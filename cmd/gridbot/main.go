package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"grid-trading/internal/alert"
	"grid-trading/internal/backtest"
	"grid-trading/internal/config"
	"grid-trading/internal/core"
	"grid-trading/internal/engine"
	"grid-trading/internal/exchange/binance"
	"grid-trading/internal/safety"
	"grid-trading/internal/store"
	"grid-trading/internal/strategy"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "config/config.yaml", "config yaml path")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		fatal(err.Error())
	}
	alerts := buildAlertManager(cfg)
	if alerts != nil {
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := alerts.Close(closeCtx); err != nil {
				fmt.Fprintf(os.Stderr, "close alert manager failed: %v\n", err)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var st *store.Store
	var instanceLock *store.InstanceLock
	if cfg.Mode != config.ModeBacktest && cfg.State.Dir != "" {
		stateDir := filepath.Join(cfg.State.Dir, strings.ToLower(string(cfg.Mode)), cfg.Symbol, cfg.InstanceID)
		st, err = store.New(stateDir)
		if err != nil {
			fatal(err.Error())
		}
		lockTakeover := true
		if cfg.State.LockTakeover != nil {
			lockTakeover = *cfg.State.LockTakeover
		}
		instanceLock, err = store.AcquireInstanceLockWithOptions(stateDir, store.LockOptions{
			TakeoverEnabled: lockTakeover,
			StaleAfter:      time.Duration(cfg.State.LockStaleSec) * time.Second,
		})
		if err != nil {
			fatal(err.Error())
		}
		defer func() {
			if relErr := instanceLock.Release(); relErr != nil {
				fmt.Fprintf(os.Stderr, "release instance lock failed: %v\n", relErr)
			}
		}()
	}
	switch cfg.Mode {
	case config.ModeBacktest:
		feed, err := backtest.NewJSONLFeed(cfg.Backtest.DataPath)
		if err != nil {
			fatal(err.Error())
		}
		ex := backtest.NewSimExchange(cfg.Symbol, core.Balance{
			Base:  cfg.Backtest.InitialBase.Decimal,
			Quote: cfg.Backtest.InitialQuote.Decimal,
		}, core.Rules{})
		if err := ex.SetFees(cfg.Backtest.Fees.MakerRate.Decimal, cfg.Backtest.Fees.TakerRate.Decimal); err != nil {
			fatal(err.Error())
		}
		rules := core.Rules{
			MinQty:      cfg.Backtest.Rules.MinQty.Decimal,
			MinNotional: cfg.Backtest.Rules.MinNotional.Decimal,
			PriceTick:   cfg.Backtest.Rules.PriceTick.Decimal,
			QtyStep:     cfg.Backtest.Rules.QtyStep.Decimal,
		}
		strat := strategy.NewSpotDual(cfg.Symbol, cfg.Grid.StopPrice.Decimal, cfg.Grid.Ratio.Decimal, cfg.Grid.Levels, cfg.Grid.ShiftLevels, cfg.Grid.Qty.Decimal, cfg.Grid.MinQtyMultiple, rules, nil, ex)
		applySpotDualTuning(strat, cfg)
		runner := engine.BacktestRunner{Exchange: ex, Feed: feed, Strategy: strat}
		result, err := runner.Run(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Println("backtest canceled")
				return
			}
			fatal(err.Error())
		}
		fmt.Printf(
			"summary instance=%s trades=%d market_buy_count=%d market_buy_qty=%s total_return_pct=%s equity_return_pct=%s profit_quote=%s max_locked_capital_quote=%s max_drawdown_pct=%s max_drawdown_quote=%s capital_drawdown_pct=%s max_capital_usage_pct=%s start_equity_quote=%s end_equity_quote=%s fees_paid_quote=%s final_base=%s final_quote=%s\n",
			cfg.InstanceID,
			result.Trades,
			result.MarketBuyCount,
			result.MarketBuyQty.String(),
			result.TotalReturnPct.StringFixed(4),
			result.EquityReturnPct.StringFixed(4),
			result.ProfitQuote.String(),
			result.MaxLockedCapital.String(),
			result.MaxDrawdownPct.StringFixed(4),
			result.MaxDrawdownQuote.String(),
			result.CapitalDrawdownPct.StringFixed(4),
			result.MaxCapitalUsagePct.StringFixed(4),
			result.StartEquityQuote.String(),
			result.EndEquityQuote.String(),
			result.FeesPaidQuote.String(),
			result.FinalBalance.Base.String(),
			result.FinalBalance.Quote.String(),
		)
	case config.ModeTestnet, config.ModeLive:
		client, err := binance.NewClient(cfg.Exchange, cfg.Symbol, cfg.InstanceID)
		if err != nil {
			fatal(err.Error())
		}
		defer client.Close()
		client.SetAlerter(alerts)
		rules, err := client.GetRules(ctx, cfg.Symbol)
		if err != nil {
			fatal(err.Error())
		}
		breaker := safety.NewBreaker(
			cfg.CircuitBreaker.Enabled,
			cfg.CircuitBreaker.MaxPlaceFailures,
			cfg.CircuitBreaker.MaxCancelFailures,
			cfg.CircuitBreaker.MaxReconnectFailures,
		)
		breaker.SetReconnectRecovery(
			time.Duration(cfg.CircuitBreaker.ReconnectCooldownSec)*time.Second,
			cfg.CircuitBreaker.ReconnectProbePasses,
		)
		breaker.SetAlerter(alerts)
		exec := safety.NewGuardedExecutor(client, breaker)
		strat := strategy.NewSpotDual(cfg.Symbol, cfg.Grid.StopPrice.Decimal, cfg.Grid.Ratio.Decimal, cfg.Grid.Levels, cfg.Grid.ShiftLevels, cfg.Grid.Qty.Decimal, cfg.Grid.MinQtyMultiple, rules, st, exec)
		applySpotDualTuning(strat, cfg)
		strat.SetAlerter(alerts)
		if st != nil {
			if state, ok, err := st.LoadGridState(); err != nil {
				fatal(err.Error())
			} else if ok {
				strat.LoadState(state)
			}
		}
		runner := engine.LiveRunner{
			Exchange:   client,
			Strategy:   strat,
			Symbol:     cfg.Symbol,
			Mode:       string(cfg.Mode),
			InstanceID: cfg.InstanceID,
			Keepalive:  time.Duration(cfg.Exchange.UserStreamKeepaliveSec) * time.Second,
			Heartbeat:  time.Duration(cfg.Observability.Runtime.HeartbeatSec) * time.Second,
			Reconcile:  time.Duration(cfg.Observability.Runtime.ReconcileIntervalSec) * time.Second,
			Store:      st,
			Breaker:    breaker,
			Alerts:     alerts,
		}
		if err := runner.Run(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			fatal(err.Error())
		}
	default:
		fatal("unknown mode")
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

func buildAlertManager(cfg config.Config) *alert.Manager {
	tg := cfg.Observability.Telegram
	if !tg.Enabled {
		return nil
	}
	notifier := alert.NewTelegramNotifier(
		tg.Enabled,
		tg.BotToken,
		tg.ChatID,
		tg.APIBaseURL,
		time.Duration(tg.TimeoutSec)*time.Second,
	)
	return alert.NewManagerWithOptions(string(cfg.Mode), cfg.Symbol, notifier, alert.ManagerOptions{
		DropReportInterval: time.Duration(cfg.Observability.Runtime.AlertDropReportSec) * time.Second,
	})
}

func applySpotDualTuning(strat *strategy.SpotDual, cfg config.Config) {
	if strat == nil {
		return
	}
	strat.SetSellRatio(cfg.Grid.SellRatio.Decimal)
}
