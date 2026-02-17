package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"grid-trading/internal/config"
	"grid-trading/internal/core"
	"grid-trading/internal/exchange/binance"
	"grid-trading/internal/strategy"
)

type checkStatus string

const (
	statusPass checkStatus = "PASS"
	statusFail checkStatus = "FAIL"
)

type checkResult struct {
	Name       string      `json:"name"`
	Status     checkStatus `json:"status"`
	DurationMs int64       `json:"duration_ms"`
	Detail     string      `json:"detail,omitempty"`
	Error      string      `json:"error,omitempty"`
}

type report struct {
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Mode       config.Mode   `json:"mode"`
	Symbol     string        `json:"symbol"`
	Checks     []checkResult `json:"checks"`
}

type selectedChecks struct {
	preflight bool
	lifecycle bool
	stream    bool
	reconnect bool
	bootstrap bool
}

func main() {
	var (
		configPath   string
		timeoutSec   int
		streamWait   int
		outJSONPath  string
		allowLiveRun bool
		checkFlag    string
	)
	flag.StringVar(&configPath, "config", "config/config.yaml", "config yaml path")
	flag.IntVar(&timeoutSec, "timeout-sec", 180, "total timeout seconds")
	flag.IntVar(&streamWait, "stream-wait-sec", 10, "wait seconds for user stream checks")
	flag.StringVar(&outJSONPath, "out-json", "", "optional output report path")
	flag.BoolVar(&allowLiveRun, "allow-live", false, "allow running checks when mode=live")
	flag.StringVar(&checkFlag, "check", "default", "checks to run: default | all | bootstrap | comma list (preflight,lifecycle,stream,reconnect,bootstrap)")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		fatal(err.Error())
	}
	if cfg.Mode != config.ModeTestnet && cfg.Mode != config.ModeLive {
		fatal("testnetcheck requires mode=testnet or mode=live")
	}
	if cfg.Mode == config.ModeLive && !allowLiveRun {
		fatal("mode=live blocked by default; set -allow-live=true to continue")
	}
	checks, err := parseCheckFlag(checkFlag)
	if err != nil {
		fatal(err.Error())
	}

	if timeoutSec < 30 {
		timeoutSec = 30
	}
	if streamWait < 3 {
		streamWait = 3
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	client, err := binance.NewClient(cfg.Exchange, cfg.Symbol, cfg.InstanceID)
	if err != nil {
		fatal(err.Error())
	}
	defer client.Close()

	r := report{
		StartedAt: time.Now().UTC(),
		Mode:      cfg.Mode,
		Symbol:    cfg.Symbol,
	}

	var (
		marketLoaded bool
		rules        core.Rules
		lastPrice    decimal.Decimal
		lastQuote    decimal.Decimal
		placedID     string
		placedCID    string
		placedSide   core.Side
	)

	loadMarketContext := func() error {
		if marketLoaded {
			return nil
		}
		var err error
		rules, err = client.GetRules(ctx, cfg.Symbol)
		if err != nil {
			return err
		}
		lastPrice, err = client.TickerPrice(ctx, cfg.Symbol)
		if err != nil {
			return err
		}
		bal, err := client.Balances(ctx)
		if err != nil {
			return err
		}
		lastQuote = bal.Quote
		marketLoaded = true
		return nil
	}

	run := func(name string, fn func() (string, error)) {
		start := time.Now()
		detail, err := fn()
		cr := checkResult{
			Name:       name,
			DurationMs: time.Since(start).Milliseconds(),
			Detail:     detail,
		}
		if err != nil {
			cr.Status = statusFail
			cr.Error = err.Error()
		} else {
			cr.Status = statusPass
		}
		r.Checks = append(r.Checks, cr)
		if cr.Status == statusPass {
			fmt.Printf("[PASS] %s (%dms)", name, cr.DurationMs)
			if cr.Detail != "" {
				fmt.Printf(" - %s", cr.Detail)
			}
			fmt.Println()
		} else {
			fmt.Printf("[FAIL] %s (%dms) - %s\n", name, cr.DurationMs, cr.Error)
		}
	}

	if checks.preflight {
		run("exchange_preflight", func() (string, error) {
			if err := loadMarketContext(); err != nil {
				return "", err
			}
			return fmt.Sprintf("price=%s minQty=%s minNotional=%s quoteBalance=%s", lastPrice.String(), rules.MinQty.String(), rules.MinNotional.String(), lastQuote.String()), nil
		})
	}

	if checks.lifecycle {
		run("order_lifecycle_place_query_cancel", func() (string, error) {
			if err := loadMarketContext(); err != nil {
				return "", err
			}
			if lastPrice.Cmp(decimal.Zero) <= 0 {
				return "", errors.New("missing ticker price")
			}
			price := lastPrice.Mul(decimal.RequireFromString("0.5"))
			if rules.PriceTick.Cmp(decimal.Zero) > 0 {
				price = core.RoundDown(price, rules.PriceTick)
			}
			if price.Cmp(decimal.Zero) <= 0 {
				return "", errors.New("calculated order price <= 0")
			}
			qty, err := buildTinyLimitQty(cfg, rules, price)
			if err != nil {
				return "", err
			}
			notional := price.Mul(qty)
			if lastQuote.Cmp(notional) < 0 {
				return "", fmt.Errorf("insufficient quote for check order: need=%s have=%s", notional.String(), lastQuote.String())
			}

			order := core.Order{
				Symbol: cfg.Symbol,
				Side:   core.Buy,
				Type:   core.Limit,
				Price:  price,
				Qty:    qty,
			}
			placed, err := client.PlaceOrder(ctx, order)
			if err != nil {
				return "", err
			}
			if placed.ID == "" {
				return "", errors.New("empty order id")
			}
			placedID = placed.ID
			placedCID = placed.ClientID
			placedSide = placed.Side

			query, err := client.QueryOrder(ctx, cfg.Symbol, placed.ID, placed.ClientID)
			if err != nil {
				return "", err
			}

			open, err := client.OpenOrders(ctx, cfg.Symbol)
			if err != nil {
				return "", err
			}
			foundInOpen := false
			for _, ord := range open {
				if ord.ID == placed.ID {
					foundInOpen = true
					break
				}
			}

			status := string(query.Order.Status)
			switch query.Order.Status {
			case core.OrderNew, core.OrderPartiallyFilled:
				if err := client.CancelOrder(ctx, cfg.Symbol, placed.ID); err != nil {
					return "", fmt.Errorf("cancel order failed: %w", err)
				}
				time.Sleep(400 * time.Millisecond)
				queryAfter, err := client.QueryOrder(ctx, cfg.Symbol, placed.ID, placed.ClientID)
				if err == nil {
					status = string(queryAfter.Order.Status)
				}
			case core.OrderFilled:
				// Unexpected for a far-below-market order but acceptable for lifecycle check.
			default:
				// keep status for report
			}

			return fmt.Sprintf("id=%s clientId=%s side=%s qty=%s price=%s status=%s foundInOpen=%t", placedID, placedCID, placedSide, qty.String(), price.String(), status, foundInOpen), nil
		})
	}

	if checks.stream {
		run("user_stream_subscribe", func() (string, error) {
			cctx, ccancel := context.WithTimeout(ctx, time.Duration(streamWait)*time.Second)
			defer ccancel()

			stream, err := client.NewUserStream(cctx, time.Duration(cfg.Exchange.UserStreamKeepaliveSec)*time.Second)
			if err != nil {
				return "", err
			}
			trades, errs := stream.Trades(cctx, cfg.Symbol)
			count := 0
			for {
				select {
				case <-cctx.Done():
					if errors.Is(cctx.Err(), context.DeadlineExceeded) {
						return fmt.Sprintf("no stream errors during %ds window trades=%d", streamWait, count), nil
					}
					return "", cctx.Err()
				case t, ok := <-trades:
					if !ok {
						return "", errors.New("trades channel closed unexpectedly")
					}
					if t.OrderID != "" {
						count++
					}
				case err, ok := <-errs:
					if ok && err != nil {
						return "", err
					}
				}
			}
		})
	}

	if checks.reconnect {
		run("user_stream_reconnect", func() (string, error) {
			okRounds := 0
			for i := 0; i < 2; i++ {
				roundCtx, roundCancel := context.WithTimeout(ctx, 5*time.Second)
				stream, err := client.NewUserStream(roundCtx, time.Duration(cfg.Exchange.UserStreamKeepaliveSec)*time.Second)
				if err != nil {
					roundCancel()
					return "", fmt.Errorf("round %d subscribe failed: %w", i+1, err)
				}
				_, errs := stream.Trades(roundCtx, cfg.Symbol)
				select {
				case err, ok := <-errs:
					roundCancel()
					if ok && err != nil {
						return "", fmt.Errorf("round %d stream error: %w", i+1, err)
					}
					return "", fmt.Errorf("round %d stream closed unexpectedly", i+1)
				case <-time.After(2 * time.Second):
					okRounds++
					roundCancel()
					time.Sleep(300 * time.Millisecond)
				case <-ctx.Done():
					roundCancel()
					return "", ctx.Err()
				}
			}
			return fmt.Sprintf("reconnect rounds passed=%d", okRounds), nil
		})
	}

	if checks.bootstrap {
		run("strategy_bootstrap_distribution", func() (string, error) {
			if err := loadMarketContext(); err != nil {
				return "", err
			}
			return runBootstrapDistributionCheck(ctx, cfg, client, rules, lastPrice)
		})
	}

	// cleanup: if lifecycle order still exists, best-effort cancel
	if placedID != "" {
		_ = client.CancelOrder(context.Background(), cfg.Symbol, placedID)
	}

	r.FinishedAt = time.Now().UTC()
	printSummary(r)

	if outJSONPath != "" {
		if err := writeReport(outJSONPath, r); err != nil {
			fatal(err.Error())
		}
		fmt.Printf("report written: %s\n", outJSONPath)
	}

	for _, c := range r.Checks {
		if c.Status == statusFail {
			os.Exit(1)
		}
	}
}

func parseCheckFlag(raw string) (selectedChecks, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" || raw == "default" {
		return selectedChecks{
			preflight: true,
			lifecycle: true,
			stream:    true,
			reconnect: true,
		}, nil
	}
	if raw == "all" {
		return selectedChecks{
			preflight: true,
			lifecycle: true,
			stream:    true,
			reconnect: true,
			bootstrap: true,
		}, nil
	}

	var out selectedChecks
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		name := strings.TrimSpace(p)
		switch name {
		case "":
			continue
		case "preflight", "exchange_preflight":
			out.preflight = true
		case "lifecycle", "order_lifecycle", "order_lifecycle_place_query_cancel":
			out.lifecycle = true
		case "stream", "user_stream", "user_stream_subscribe":
			out.stream = true
		case "reconnect", "user_stream_reconnect":
			out.reconnect = true
		case "bootstrap", "strategy_bootstrap", "strategy_bootstrap_distribution":
			out.bootstrap = true
		default:
			return selectedChecks{}, fmt.Errorf("unknown check: %s", name)
		}
	}
	if !out.preflight && !out.lifecycle && !out.stream && !out.reconnect && !out.bootstrap {
		return selectedChecks{}, errors.New("no checks selected")
	}
	return out, nil
}

func runBootstrapDistributionCheck(ctx context.Context, cfg config.Config, client *binance.Client, rules core.Rules, anchor decimal.Decimal) (string, error) {
	if anchor.Cmp(decimal.Zero) <= 0 {
		return "", errors.New("invalid anchor price")
	}
	before, err := client.OpenOrders(ctx, cfg.Symbol)
	if err != nil {
		return "", err
	}

	strat := strategy.NewSpotDual(
		cfg.Symbol,
		cfg.Grid.StopPrice.Decimal,
		cfg.Grid.Ratio.Decimal,
		cfg.Grid.Levels,
		cfg.Grid.ShiftLevels,
		cfg.Grid.Qty.Decimal,
		cfg.Grid.MinQtyMultiple,
		rules,
		nil,
		client,
	)
	strat.SetSellRatio(cfg.Grid.SellRatio.Decimal)
	strat.SetRegimeControl(strategy.RegimeControlConfig{
		Enabled:                  cfg.Grid.Regime.Enabled,
		Window:                   cfg.Grid.Regime.Window,
		EnterScore:               cfg.Grid.Regime.EnterScore.InexactFloat64(),
		ExitScore:                cfg.Grid.Regime.ExitScore.InexactFloat64(),
		EnterConfirm:             cfg.Grid.Regime.EnterConfirm,
		ExitConfirm:              cfg.Grid.Regime.ExitConfirm,
		MinDwell:                 time.Duration(cfg.Grid.Regime.MinDwellSec) * time.Second,
		TrendUpBuySpacingMult:    cfg.Grid.Regime.TrendUpBuySpacingMult.InexactFloat64(),
		TrendDownBuySpacingMult:  cfg.Grid.Regime.TrendDownBuySpacingMult.InexactFloat64(),
		TrendDownSellSpacingMult: cfg.Grid.Regime.TrendDownSellSpacingMult.InexactFloat64(),
		TrendUpSellQtyFactor:     cfg.Grid.Regime.TrendUpSellQtyFactor.InexactFloat64(),
	})
	initErr := strat.Init(ctx, anchor)

	after, err := client.OpenOrders(ctx, cfg.Symbol)
	if err != nil {
		return "", err
	}
	added := diffOrders(before, after)
	cancelAttempts, cancelFailures := cancelOrdersBestEffort(cfg.Symbol, added, client)

	if initErr != nil {
		return "", fmt.Errorf("bootstrap init failed: %w (new_orders=%d canceled=%d cancel_failures=%d)", initErr, len(added), cancelAttempts-cancelFailures, cancelFailures)
	}
	if len(added) == 0 {
		return "", errors.New("bootstrap placed no new open orders")
	}

	expectedSell := cfg.Grid.ShiftLevels
	expectedBuy := cfg.Grid.Levels
	expectedTotal := expectedSell + expectedBuy
	if len(added) != expectedTotal {
		return "", fmt.Errorf("unexpected bootstrap open order count: expected=%d got=%d", expectedTotal, len(added))
	}

	sellObs := map[string]int{}
	buyObs := map[string]int{}
	sellCount := 0
	buyCount := 0
	for _, ord := range added {
		switch ord.Side {
		case core.Sell:
			sellCount++
			sellObs[ord.Price.String()]++
		case core.Buy:
			buyCount++
			buyObs[ord.Price.String()]++
		}
	}
	if sellCount != expectedSell || buyCount != expectedBuy {
		return "", fmt.Errorf("unexpected side distribution: expected sell=%d buy=%d, got sell=%d buy=%d", expectedSell, expectedBuy, sellCount, buyCount)
	}

	for i := 1; i <= expectedSell; i++ {
		p := priceForLevel(anchor, cfg.Grid.SellRatio.Decimal, i, rules.PriceTick).String()
		if sellObs[p] <= 0 {
			return "", fmt.Errorf("missing expected sell level=%d price=%s", i, p)
		}
		sellObs[p]--
	}
	for i := -1; i >= -cfg.Grid.Levels; i-- {
		p := priceForLevel(anchor, cfg.Grid.Ratio.Decimal, i, rules.PriceTick).String()
		if buyObs[p] <= 0 {
			return "", fmt.Errorf("missing expected buy level=%d price=%s", i, p)
		}
		buyObs[p]--
	}

	return fmt.Sprintf(
		"anchor=%s levels=%d shift_levels=%d expected(total/sell/buy)=%d/%d/%d observed=%d/%d/%d canceled=%d cancel_failures=%d",
		anchor.String(),
		cfg.Grid.Levels,
		cfg.Grid.ShiftLevels,
		expectedTotal,
		expectedSell,
		expectedBuy,
		len(added),
		sellCount,
		buyCount,
		cancelAttempts-cancelFailures,
		cancelFailures,
	), nil
}

func diffOrders(before, after []core.Order) []core.Order {
	beforeIDs := make(map[string]struct{}, len(before))
	for _, ord := range before {
		if ord.ID != "" {
			beforeIDs[ord.ID] = struct{}{}
		}
	}
	out := make([]core.Order, 0)
	for _, ord := range after {
		if ord.ID == "" {
			continue
		}
		if _, ok := beforeIDs[ord.ID]; ok {
			continue
		}
		out = append(out, ord)
	}
	return out
}

func cancelOrdersBestEffort(symbol string, orders []core.Order, client *binance.Client) (attempts int, failures int) {
	if len(orders) == 0 {
		return 0, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, ord := range orders {
		if ord.ID == "" {
			continue
		}
		attempts++
		if err := client.CancelOrder(ctx, symbol, ord.ID); err != nil {
			failures++
		}
	}
	return attempts, failures
}

func priceForLevel(anchor, ratio decimal.Decimal, idx int, tick decimal.Decimal) decimal.Decimal {
	price := anchor.Mul(powDecimal(ratio, idx))
	if tick.Cmp(decimal.Zero) > 0 {
		price = core.RoundDown(price, tick)
	}
	return price
}

func powDecimal(base decimal.Decimal, exp int) decimal.Decimal {
	if exp == 0 {
		return decimal.NewFromInt(1)
	}
	if exp < 0 {
		return decimal.NewFromInt(1).Div(powDecimal(base, -exp))
	}
	out := decimal.NewFromInt(1)
	for i := 0; i < exp; i++ {
		out = out.Mul(base)
	}
	return out
}

func buildTinyLimitQty(cfg config.Config, rules core.Rules, price decimal.Decimal) (decimal.Decimal, error) {
	if price.Cmp(decimal.Zero) <= 0 {
		return decimal.Zero, errors.New("invalid price")
	}

	qty := cfg.Grid.Qty.Decimal
	if rules.MinQty.Cmp(decimal.Zero) > 0 {
		minByMulti := rules.MinQty.Mul(decimal.NewFromInt(cfg.Grid.MinQtyMultiple))
		if qty.Cmp(minByMulti) < 0 {
			qty = minByMulti
		}
	}
	if rules.MinNotional.Cmp(decimal.Zero) > 0 {
		minNotionalQty := rules.MinNotional.Div(price)
		if minNotionalQty.Cmp(qty) > 0 {
			qty = minNotionalQty
		}
	}
	qty = roundQtyUp(qty, rules.QtyStep)
	if rules.MinQty.Cmp(decimal.Zero) > 0 && qty.Cmp(rules.MinQty) < 0 {
		qty = rules.MinQty
	}
	if qty.Cmp(decimal.Zero) <= 0 {
		return decimal.Zero, errors.New("calculated qty <= 0")
	}
	if rules.MinNotional.Cmp(decimal.Zero) > 0 {
		notional := price.Mul(qty)
		if notional.Cmp(rules.MinNotional) < 0 {
			qty = roundQtyUp(rules.MinNotional.Div(price), rules.QtyStep)
		}
	}
	norm, err := core.NormalizeOrder(core.Order{
		Symbol: cfg.Symbol,
		Side:   core.Buy,
		Type:   core.Limit,
		Price:  price,
		Qty:    qty,
	}, rules)
	if err != nil {
		return decimal.Zero, err
	}
	return norm.Qty, nil
}

func roundQtyUp(qty, step decimal.Decimal) decimal.Decimal {
	if qty.Cmp(decimal.Zero) <= 0 {
		return decimal.Zero
	}
	if step.Cmp(decimal.Zero) <= 0 {
		return qty
	}
	return qty.Div(step).Ceil().Mul(step)
}

func printSummary(r report) {
	pass := 0
	fail := 0
	for _, c := range r.Checks {
		if c.Status == statusPass {
			pass++
		} else {
			fail++
		}
	}
	fmt.Printf("\nsummary mode=%s symbol=%s pass=%d fail=%d duration=%s\n",
		r.Mode,
		r.Symbol,
		pass,
		fail,
		r.FinishedAt.Sub(r.StartedAt).Round(time.Millisecond).String(),
	)
}

func writeReport(path string, r report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(msg))
	os.Exit(1)
}
