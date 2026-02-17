package engine

import (
	"context"
	"errors"
	"io"

	"github.com/shopspring/decimal"

	"grid-trading/internal/backtest"
	"grid-trading/internal/core"
	"grid-trading/internal/strategy"
)

type BacktestRunner struct {
	Exchange *backtest.SimExchange
	Feed     backtest.Feed
	Strategy strategy.Strategy
}

type BacktestResult struct {
	Trades              int
	MarketBuyCount      int
	MarketBuyQty        decimal.Decimal
	StartPrice          decimal.Decimal
	EndPrice            decimal.Decimal
	FinalBalance        core.Balance
	StartEquityQuote    decimal.Decimal
	EndEquityQuote      decimal.Decimal
	TotalReturnPct      decimal.Decimal
	MaxDrawdownPct      decimal.Decimal
	MaxDrawdownQuote    decimal.Decimal
	MaxCapitalUsagePct  decimal.Decimal
	FeesPaidQuote       decimal.Decimal
	DailyPnLQuoteSeries []DailyPnL
}

type DailyPnL struct {
	Date     string
	PnLQuote decimal.Decimal
}

func (r *BacktestRunner) Run(ctx context.Context) (BacktestResult, error) {
	var result BacktestResult
	if r.Feed != nil {
		defer r.Feed.Close()
	}
	first := true
	stopped := false
	highWatermark := decimal.Zero
	maxDrawdown := decimal.Zero
	maxDrawdownQuote := decimal.Zero
	maxUsage := decimal.Zero
	dailyClose := make(map[string]decimal.Decimal)
	dayOrder := make([]string, 0)
	tickAware, hasTickAware := r.Strategy.(strategy.TickAware)

	recordSnapshot := func(tick backtest.Tick) {
		snap := r.Exchange.Snapshot(tick.Price)
		result.FeesPaidQuote = snap.FeePaidQuote
		if result.StartEquityQuote.Cmp(decimal.Zero) == 0 {
			result.StartEquityQuote = snap.EquityQuote
		}
		if snap.EquityQuote.Cmp(highWatermark) > 0 {
			highWatermark = snap.EquityQuote
		}
		if highWatermark.Cmp(decimal.Zero) > 0 {
			drawdownQuote := highWatermark.Sub(snap.EquityQuote)
			if drawdownQuote.Cmp(maxDrawdownQuote) > 0 {
				maxDrawdownQuote = drawdownQuote
			}
			dd := drawdownQuote.Div(highWatermark)
			if dd.Cmp(maxDrawdown) > 0 {
				maxDrawdown = dd
			}
		}
		if snap.EquityQuote.Cmp(decimal.Zero) > 0 {
			usage := snap.LockedCapital.Div(snap.EquityQuote)
			if usage.Cmp(maxUsage) > 0 {
				maxUsage = usage
			}
		}
		day := tick.Time.UTC().Format("2006-01-02")
		if _, ok := dailyClose[day]; !ok {
			dayOrder = append(dayOrder, day)
		}
		dailyClose[day] = snap.EquityQuote
	}

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		tick, err := r.Feed.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return result, err
		}
		if first {
			result.StartPrice = tick.Price
			if err := r.Strategy.Init(ctx, tick.Price); err != nil {
				if errors.Is(err, strategy.ErrStopped) {
					stopped = true
					break
				}
				return result, err
			}
			recordSnapshot(tick)
			first = false
		}
		trades := r.Exchange.Match(tick.Price, tick.Time)
		for _, trade := range trades {
			result.Trades++
			if err := r.Strategy.OnFill(ctx, trade); err != nil {
				if errors.Is(err, strategy.ErrStopped) {
					stopped = true
					break
				}
				return result, err
			}
		}
		if !stopped && hasTickAware {
			if err := tickAware.OnTick(ctx, tick.Price, tick.Time); err != nil {
				if errors.Is(err, strategy.ErrStopped) {
					stopped = true
				} else {
					return result, err
				}
			}
		}
		recordSnapshot(tick)
		result.EndPrice = tick.Price
		if stopped {
			break
		}
	}
	bal, _ := r.Exchange.Balances(ctx)
	result.FinalBalance = bal
	result.MarketBuyCount, result.MarketBuyQty = r.Exchange.MarketBuyStats()
	if result.EndPrice.Cmp(decimal.Zero) > 0 {
		result.EndEquityQuote = bal.Quote.Add(bal.Base.Mul(result.EndPrice))
	}
	result.MaxDrawdownPct = maxDrawdown.Mul(decimal.NewFromInt(100))
	result.MaxDrawdownQuote = maxDrawdownQuote
	result.MaxCapitalUsagePct = maxUsage.Mul(decimal.NewFromInt(100))
	if result.StartEquityQuote.Cmp(decimal.Zero) > 0 {
		result.TotalReturnPct = result.EndEquityQuote.Sub(result.StartEquityQuote).Div(result.StartEquityQuote).Mul(decimal.NewFromInt(100))
	}
	prevClose := result.StartEquityQuote
	for _, day := range dayOrder {
		closeEquity := dailyClose[day]
		pnl := closeEquity.Sub(prevClose)
		result.DailyPnLQuoteSeries = append(result.DailyPnLQuoteSeries, DailyPnL{
			Date:     day,
			PnLQuote: pnl,
		})
		prevClose = closeEquity
	}
	return result, nil
}
