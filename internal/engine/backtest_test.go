package engine

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"spot-dual/internal/backtest"
	"spot-dual/internal/core"
	"spot-dual/internal/strategy"
)

type noopStrategy struct{}

func (noopStrategy) Init(context.Context, decimal.Decimal) error { return nil }
func (noopStrategy) OnFill(context.Context, core.Trade) error    { return nil }

type marketBuyInitStrategy struct {
	ex  *backtest.SimExchange
	qty decimal.Decimal
}

func (s *marketBuyInitStrategy) Init(ctx context.Context, price decimal.Decimal) error {
	_, err := s.ex.PlaceOrder(ctx, core.Order{
		Symbol: "BTCUSDT",
		Side:   core.Buy,
		Type:   core.Market,
		Price:  price,
		Qty:    s.qty,
	})
	return err
}

func (s *marketBuyInitStrategy) OnFill(context.Context, core.Trade) error { return nil }

type buyAndHoldLimitStrategy struct {
	ex  *backtest.SimExchange
	qty decimal.Decimal
}

func (s *buyAndHoldLimitStrategy) Init(ctx context.Context, price decimal.Decimal) error {
	_, err := s.ex.PlaceOrder(ctx, core.Order{
		Symbol: "BTCUSDT",
		Side:   core.Buy,
		Type:   core.Limit,
		Price:  price,
		Qty:    s.qty,
	})
	return err
}

func (s *buyAndHoldLimitStrategy) OnFill(context.Context, core.Trade) error { return nil }

type tickAwareSpy struct {
	prices []decimal.Decimal
	times  []time.Time
	stopAt int
}

func (s *tickAwareSpy) Init(context.Context, decimal.Decimal) error { return nil }

func (s *tickAwareSpy) OnFill(context.Context, core.Trade) error { return nil }

func (s *tickAwareSpy) OnTick(_ context.Context, price decimal.Decimal, at time.Time) error {
	s.prices = append(s.prices, price)
	s.times = append(s.times, at)
	if s.stopAt > 0 && len(s.prices) == s.stopAt {
		return strategy.ErrStopped
	}
	return nil
}

type stubFeed struct {
	closeCalled bool
}

func (f *stubFeed) Next() (backtest.Tick, error) {
	return backtest.Tick{}, io.EOF
}

func (f *stubFeed) Close() error {
	f.closeCalled = true
	return nil
}

func TestBacktestRunnerHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	feed := &stubFeed{}
	runner := BacktestRunner{
		Exchange: backtest.NewSimExchange(
			"BTCUSDT",
			core.Balance{Base: decimal.Zero, Quote: decimal.NewFromInt(1000)},
			core.Rules{},
		),
		Feed:     feed,
		Strategy: noopStrategy{},
	}

	_, err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
	if !feed.closeCalled {
		t.Fatalf("feed.Close() was not called")
	}
}

func TestBacktestRunnerProcessesSingleTick(t *testing.T) {
	feed := &singleTickFeed{
		tick: backtest.Tick{
			Time:  time.Unix(0, 0).UTC(),
			Price: decimal.NewFromInt(100),
		},
	}
	runner := BacktestRunner{
		Exchange: backtest.NewSimExchange(
			"BTCUSDT",
			core.Balance{Base: decimal.Zero, Quote: decimal.NewFromInt(1000)},
			core.Rules{},
		),
		Feed:     feed,
		Strategy: noopStrategy{},
	}
	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !res.StartPrice.Equal(decimal.NewFromInt(100)) || !res.EndPrice.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("unexpected prices start=%s end=%s", res.StartPrice, res.EndPrice)
	}
	if !feed.closeCalled {
		t.Fatalf("feed.Close() was not called")
	}
}

func TestBacktestRunnerCallsTickAwareOnEachTick(t *testing.T) {
	t0 := time.Unix(10, 0).UTC()
	feed := &multiTickFeed{
		ticks: []backtest.Tick{
			{Time: t0, Price: decimal.NewFromInt(100)},
			{Time: t0.Add(time.Minute), Price: decimal.NewFromInt(101)},
			{Time: t0.Add(2 * time.Minute), Price: decimal.NewFromInt(102)},
		},
	}
	spy := &tickAwareSpy{}
	runner := BacktestRunner{
		Exchange: backtest.NewSimExchange(
			"BTCUSDT",
			core.Balance{Base: decimal.Zero, Quote: decimal.NewFromInt(1000)},
			core.Rules{},
		),
		Feed:     feed,
		Strategy: spy,
	}
	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(spy.prices) != 3 {
		t.Fatalf("OnTick call count = %d, want 3", len(spy.prices))
	}
	for i, want := range []decimal.Decimal{
		decimal.NewFromInt(100),
		decimal.NewFromInt(101),
		decimal.NewFromInt(102),
	} {
		if !spy.prices[i].Equal(want) {
			t.Fatalf("OnTick price[%d] = %s, want %s", i, spy.prices[i], want)
		}
	}
	if !spy.times[0].Equal(t0) {
		t.Fatalf("OnTick time[0] = %s, want %s", spy.times[0], t0)
	}
	if !res.EndPrice.Equal(decimal.NewFromInt(102)) {
		t.Fatalf("EndPrice = %s, want 102", res.EndPrice)
	}
	if !feed.closeCalled {
		t.Fatalf("feed.Close() was not called")
	}
}

func TestBacktestRunnerStopsWhenTickAwareReturnsStopped(t *testing.T) {
	t0 := time.Unix(20, 0).UTC()
	feed := &multiTickFeed{
		ticks: []backtest.Tick{
			{Time: t0, Price: decimal.NewFromInt(100)},
			{Time: t0.Add(time.Minute), Price: decimal.NewFromInt(101)},
			{Time: t0.Add(2 * time.Minute), Price: decimal.NewFromInt(102)},
		},
	}
	spy := &tickAwareSpy{stopAt: 2}
	runner := BacktestRunner{
		Exchange: backtest.NewSimExchange(
			"BTCUSDT",
			core.Balance{Base: decimal.Zero, Quote: decimal.NewFromInt(1000)},
			core.Rules{},
		),
		Feed:     feed,
		Strategy: spy,
	}
	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(spy.prices) != 2 {
		t.Fatalf("OnTick call count = %d, want 2", len(spy.prices))
	}
	if !res.EndPrice.Equal(decimal.NewFromInt(101)) {
		t.Fatalf("EndPrice = %s, want 101", res.EndPrice)
	}
	if !feed.closeCalled {
		t.Fatalf("feed.Close() was not called")
	}
}

func TestBacktestRunnerTracksMaxDrawdownPctAndQuote(t *testing.T) {
	t0 := time.Unix(30, 0).UTC()
	feed := &multiTickFeed{
		ticks: []backtest.Tick{
			{Time: t0, Price: decimal.NewFromInt(100)},
			{Time: t0.Add(time.Minute), Price: decimal.NewFromInt(80)},
			{Time: t0.Add(2 * time.Minute), Price: decimal.NewFromInt(120)},
		},
	}
	runner := BacktestRunner{
		Exchange: backtest.NewSimExchange(
			"BTCUSDT",
			core.Balance{Base: decimal.NewFromInt(1), Quote: decimal.Zero},
			core.Rules{},
		),
		Feed:     feed,
		Strategy: noopStrategy{},
	}
	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !res.MaxDrawdownPct.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("MaxDrawdownPct = %s, want 20", res.MaxDrawdownPct)
	}
	if !res.MaxDrawdownQuote.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("MaxDrawdownQuote = %s, want 20", res.MaxDrawdownQuote)
	}
}

func TestBacktestRunnerTracksMarketBuyStats(t *testing.T) {
	feed := &singleTickFeed{
		tick: backtest.Tick{
			Time:  time.Unix(40, 0).UTC(),
			Price: decimal.NewFromInt(100),
		},
	}
	ex := backtest.NewSimExchange(
		"BTCUSDT",
		core.Balance{Base: decimal.Zero, Quote: decimal.NewFromInt(1000)},
		core.Rules{},
	)
	strat := &marketBuyInitStrategy{
		ex:  ex,
		qty: decimal.NewFromInt(1),
	}
	runner := BacktestRunner{
		Exchange: ex,
		Feed:     feed,
		Strategy: strat,
	}
	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.MarketBuyCount != 1 {
		t.Fatalf("MarketBuyCount = %d, want 1", res.MarketBuyCount)
	}
	if !res.MarketBuyQty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("MarketBuyQty = %s, want 1", res.MarketBuyQty)
	}
}

func TestBacktestRunnerUsesMaxLockedCapitalForTotalReturnPct(t *testing.T) {
	t0 := time.Unix(50, 0).UTC()
	feed := &multiTickFeed{
		ticks: []backtest.Tick{
			{Time: t0, Price: decimal.NewFromInt(100)},
			{Time: t0.Add(time.Minute), Price: decimal.NewFromInt(80)},
			{Time: t0.Add(2 * time.Minute), Price: decimal.NewFromInt(120)},
		},
	}
	ex := backtest.NewSimExchange(
		"BTCUSDT",
		core.Balance{Base: decimal.Zero, Quote: decimal.NewFromInt(1000)},
		core.Rules{},
	)
	strat := &buyAndHoldLimitStrategy{
		ex:  ex,
		qty: decimal.NewFromInt(1),
	}
	runner := BacktestRunner{
		Exchange: ex,
		Feed:     feed,
		Strategy: strat,
	}
	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !res.ProfitQuote.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("ProfitQuote = %s, want 20", res.ProfitQuote)
	}
	if !res.MaxLockedCapital.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("MaxLockedCapital = %s, want 100", res.MaxLockedCapital)
	}
	if !res.EquityReturnPct.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("EquityReturnPct = %s, want 2", res.EquityReturnPct)
	}
	if !res.TotalReturnPct.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("TotalReturnPct = %s, want 20", res.TotalReturnPct)
	}
	if !res.CapitalDrawdownPct.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("CapitalDrawdownPct = %s, want 20", res.CapitalDrawdownPct)
	}
}

type singleTickFeed struct {
	tick        backtest.Tick
	emitted     bool
	closeCalled bool
}

func (f *singleTickFeed) Next() (backtest.Tick, error) {
	if f.emitted {
		return backtest.Tick{}, io.EOF
	}
	f.emitted = true
	return f.tick, nil
}

func (f *singleTickFeed) Close() error {
	f.closeCalled = true
	return nil
}

type multiTickFeed struct {
	ticks       []backtest.Tick
	idx         int
	closeCalled bool
}

func (f *multiTickFeed) Next() (backtest.Tick, error) {
	if f.idx >= len(f.ticks) {
		return backtest.Tick{}, io.EOF
	}
	tick := f.ticks[f.idx]
	f.idx++
	return tick, nil
}

func (f *multiTickFeed) Close() error {
	f.closeCalled = true
	return nil
}
