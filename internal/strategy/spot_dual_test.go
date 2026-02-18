package strategy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"grid-trading/internal/core"
	"grid-trading/internal/store"
)

type fakeExecutor struct {
	nextID   int
	placed   []core.Order
	canceled []string
	balance  core.Balance

	cancelErrByID map[string]error
	cancelErr     error
}

func (f *fakeExecutor) PlaceOrder(_ context.Context, order core.Order) (core.Order, error) {
	f.nextID++
	order.ID = fmt.Sprintf("o-%d", f.nextID)
	if order.Type == core.Market {
		switch order.Side {
		case core.Buy:
			f.balance.Base = f.balance.Base.Add(order.Qty)
		case core.Sell:
			f.balance.Base = f.balance.Base.Sub(order.Qty)
		}
	}
	f.placed = append(f.placed, order)
	return order, nil
}

func (f *fakeExecutor) CancelOrder(_ context.Context, _ string, orderID string) error {
	f.canceled = append(f.canceled, orderID)
	if f.cancelErrByID != nil {
		if err, ok := f.cancelErrByID[orderID]; ok {
			return err
		}
	}
	if f.cancelErr != nil {
		return f.cancelErr
	}
	return nil
}

func (f *fakeExecutor) Balances(_ context.Context) (core.Balance, error) {
	return f.balance, nil
}

type insufficientDuringRebuildExecutor struct {
	fakeExecutor
	failMarketBuy bool
	failBuyLimit  bool
}

func (f *insufficientDuringRebuildExecutor) PlaceOrder(ctx context.Context, order core.Order) (core.Order, error) {
	if f.failMarketBuy && order.Type == core.Market && order.Side == core.Buy {
		return core.Order{}, errors.New("insufficient quote balance")
	}
	if f.failBuyLimit && order.Type == core.Limit && order.Side == core.Buy {
		return core.Order{}, errors.New("insufficient quote balance")
	}
	return f.fakeExecutor.PlaceOrder(ctx, order)
}

func newSpotDualForTest(levels, shift int, baseBalance string) (*SpotDual, *fakeExecutor) {
	exec := &fakeExecutor{
		balance: core.Balance{
			Base:  decimal.RequireFromString(baseBalance),
			Quote: decimal.NewFromInt(1_000_000),
		},
	}
	s := NewSpotDual(
		"BTCUSDT",
		decimal.Zero,
		decimal.RequireFromString("1.1"),
		levels,
		shift,
		decimal.NewFromInt(1),
		1,
		core.Rules{},
		nil,
		exec,
	)
	return s, exec
}

func findOpenOrder(s *SpotDual, side core.Side, idx int) (core.Order, bool) {
	for _, ord := range s.openOrders {
		if ord.Side == side && ord.GridIndex == idx {
			return ord, true
		}
	}
	return core.Order{}, false
}

func hasAnyOpenOrderAtLevel(s *SpotDual, idx int) bool {
	for _, ord := range s.openOrders {
		if ord.GridIndex == idx {
			return true
		}
	}
	return false
}

func TestSpotDualInitPlacesInitialOrders(t *testing.T) {
	s, _ := newSpotDualForTest(3, 1, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if !s.initialized {
		t.Fatalf("strategy should be initialized")
	}
	if s.maxLevel != 1 {
		t.Fatalf("maxLevel = %d, want 1", s.maxLevel)
	}
	if s.minLevel != -3 {
		t.Fatalf("minLevel = %d, want -3", s.minLevel)
	}
	if len(s.openOrders) != 4 {
		t.Fatalf("open orders = %d, want 4", len(s.openOrders))
	}
	for _, level := range []int{1} {
		if _, ok := findOpenOrder(s, core.Sell, level); !ok {
			t.Fatalf("missing sell order at level %d", level)
		}
	}
	for _, level := range []int{-1, -2, -3} {
		if _, ok := findOpenOrder(s, core.Buy, level); !ok {
			t.Fatalf("missing buy order at level %d", level)
		}
	}
}

func TestSpotDualPriceForLevelUsesBuyAndSellRatios(t *testing.T) {
	s, _ := newSpotDualForTest(3, 1, "10")
	s.Ratio = decimal.RequireFromString("1.01")
	s.SetSellRatio(decimal.RequireFromString("1.05"))
	s.anchor = decimal.NewFromInt(100)

	gotSell := s.priceForLevel(1)
	wantSell := decimal.RequireFromString("105")
	if !gotSell.Equal(wantSell) {
		t.Fatalf("sell price level 1 = %s, want %s", gotSell, wantSell)
	}

	gotBuy := s.priceForLevel(-1)
	wantBuy := decimal.NewFromInt(100).Div(decimal.RequireFromString("1.01"))
	if !gotBuy.Equal(wantBuy) {
		t.Fatalf("buy price level -1 = %s, want %s", gotBuy, wantBuy)
	}
}

func TestSpotDualTrendUpReducesNewSellQty(t *testing.T) {
	exec := &fakeExecutor{
		balance: core.Balance{
			Base:  decimal.RequireFromString("100"),
			Quote: decimal.RequireFromString("1000000"),
		},
	}
	s := NewSpotDual(
		"BTCUSDT",
		decimal.Zero,
		decimal.RequireFromString("1.01"),
		3,
		1,
		decimal.RequireFromString("2"),
		20,
		core.Rules{
			MinQty:  decimal.RequireFromString("0.1"),
			QtyStep: decimal.RequireFromString("0.1"),
		},
		nil,
		exec,
	)
	s.SetRegimeControl(RegimeControlConfig{Enabled: true})
	s.regimeState = RegimeTrendUp
	s.anchor = decimal.NewFromInt(100)
	s.minLevel = -3
	s.maxLevel = 2

	if err := s.placeLimit(context.Background(), core.Sell, 1); err != nil {
		t.Fatalf("placeLimit(sell) error = %v", err)
	}
	if err := s.placeLimit(context.Background(), core.Buy, -1); err != nil {
		t.Fatalf("placeLimit(buy) error = %v", err)
	}
	sell, ok := findOpenOrder(s, core.Sell, 1)
	if !ok {
		t.Fatalf("missing sell order")
	}
	buy, ok := findOpenOrder(s, core.Buy, -1)
	if !ok {
		t.Fatalf("missing buy order")
	}

	if !sell.Qty.Equal(decimal.RequireFromString("1")) {
		t.Fatalf("trend_up sell qty = %s, want 1", sell.Qty)
	}
	if !buy.Qty.Equal(decimal.RequireFromString("2")) {
		t.Fatalf("buy qty = %s, want 2", buy.Qty)
	}
}

func TestSpotDualInitBuysMissingBaseAtStartup(t *testing.T) {
	s, exec := newSpotDualForTest(3, 1, "0")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	marketBuyCount := 0
	for _, ord := range exec.placed {
		if ord.Type == core.Market && ord.Side == core.Buy {
			marketBuyCount++
			if !ord.Qty.Equal(decimal.NewFromInt(1)) {
				t.Fatalf("market buy qty = %s, want 1", ord.Qty)
			}
		}
	}
	if marketBuyCount != 1 {
		t.Fatalf("market buy count = %d, want 1", marketBuyCount)
	}
}

func TestSpotDualOnFillSellAtTopShiftsUp(t *testing.T) {
	s, exec := newSpotDualForTest(3, 1, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	topSell, ok := findOpenOrder(s, core.Sell, s.maxLevel)
	if !ok {
		t.Fatalf("missing top sell order")
	}
	oldMin := s.minLevel
	oldMax := s.maxLevel
	lowestBuy, ok := findOpenOrder(s, core.Buy, oldMin)
	if !ok {
		t.Fatalf("missing lowest buy order")
	}

	trade := core.Trade{
		OrderID: topSell.ID,
		Symbol:  s.Symbol,
		Side:    core.Sell,
		Price:   topSell.Price,
		Qty:     topSell.Qty,
		Time:    time.Now().UTC(),
	}
	if err := s.OnFill(context.Background(), trade); err != nil {
		t.Fatalf("OnFill() error = %v", err)
	}

	if s.minLevel != oldMin+1 {
		t.Fatalf("minLevel = %d, want %d", s.minLevel, oldMin+1)
	}
	if s.maxLevel != oldMax+1 {
		t.Fatalf("maxLevel = %d, want %d", s.maxLevel, oldMax+1)
	}
	if _, ok := findOpenOrder(s, core.Sell, oldMax+1); !ok {
		t.Fatalf("missing new top sell at level %d", oldMax+1)
	}
	if hasAnyOpenOrderAtLevel(s, oldMin) {
		t.Fatalf("old min level %d should be removed", oldMin)
	}
	if len(exec.canceled) != 1 || exec.canceled[0] != lowestBuy.ID {
		t.Fatalf("unexpected canceled orders: %+v, want [%s]", exec.canceled, lowestBuy.ID)
	}
	if _, ok := findOpenOrder(s, core.Buy, oldMax); !ok {
		t.Fatalf("missing replacement buy at level %d", oldMax)
	}
}

func TestSpotDualOnFillBuyAtBottomExtendsDown(t *testing.T) {
	s, _ := newSpotDualForTest(3, 1, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	bottomBuy, ok := findOpenOrder(s, core.Buy, s.minLevel)
	if !ok {
		t.Fatalf("missing bottom buy order")
	}
	oldMin := s.minLevel

	trade := core.Trade{
		OrderID: bottomBuy.ID,
		Symbol:  s.Symbol,
		Side:    core.Buy,
		Price:   bottomBuy.Price,
		Qty:     bottomBuy.Qty,
		Time:    time.Now().UTC(),
	}
	if err := s.OnFill(context.Background(), trade); err != nil {
		t.Fatalf("OnFill() error = %v", err)
	}

	wantMin := oldMin - s.Levels
	if s.minLevel != wantMin {
		t.Fatalf("minLevel = %d, want %d", s.minLevel, wantMin)
	}
	for _, level := range []int{oldMin - 1, oldMin - 2, oldMin - 3} {
		if _, ok := findOpenOrder(s, core.Buy, level); !ok {
			t.Fatalf("missing extended buy at level %d", level)
		}
	}
}

func TestSpotDualShiftUpCancelFailureKeepsFailedOrderTracked(t *testing.T) {
	s, exec := newSpotDualForTest(3, 1, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	topSell, ok := findOpenOrder(s, core.Sell, s.maxLevel)
	if !ok {
		t.Fatalf("missing top sell order")
	}
	lowestBuy, ok := findOpenOrder(s, core.Buy, s.minLevel)
	if !ok {
		t.Fatalf("missing lowest buy order")
	}
	oldMin := s.minLevel
	oldMax := s.maxLevel
	exec.cancelErrByID = map[string]error{
		lowestBuy.ID: errors.New("cancel failed"),
	}

	err := s.OnFill(context.Background(), core.Trade{
		OrderID: topSell.ID,
		Symbol:  s.Symbol,
		Side:    core.Sell,
		Price:   topSell.Price,
		Qty:     topSell.Qty,
		Time:    time.Now().UTC(),
	})
	if err == nil {
		t.Fatalf("OnFill() error = nil, want cancel failure")
	}

	if _, ok := s.openOrders[lowestBuy.ID]; !ok {
		t.Fatalf("failed-cancel order %s should remain tracked", lowestBuy.ID)
	}
	if s.minLevel != oldMin || s.maxLevel != oldMax {
		t.Fatalf("window changed on failed shift: got [%d,%d], want [%d,%d]", s.minLevel, s.maxLevel, oldMin, oldMax)
	}
}

func TestSpotDualOnFillPartialThenFilled(t *testing.T) {
	s, _ := newSpotDualForTest(3, 1, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	ord, ok := findOpenOrder(s, core.Sell, 1)
	if !ok {
		t.Fatalf("missing sell order at level 1")
	}
	initialOpen := len(s.openOrders)

	if err := s.OnFill(context.Background(), core.Trade{
		OrderID: ord.ID,
		Symbol:  s.Symbol,
		Side:    core.Sell,
		Price:   ord.Price,
		Qty:     decimal.RequireFromString("0.4"),
		Status:  core.OrderPartiallyFilled,
		Time:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("OnFill(partial) error = %v", err)
	}

	remain, ok := s.openOrders[ord.ID]
	if !ok {
		t.Fatalf("order should remain open after partial fill")
	}
	if !remain.Qty.Equal(decimal.RequireFromString("0.6")) {
		t.Fatalf("remaining qty = %s, want 0.6", remain.Qty)
	}
	if len(s.openOrders) != initialOpen {
		t.Fatalf("open orders changed unexpectedly after partial fill: got %d want %d", len(s.openOrders), initialOpen)
	}
	if _, ok := findOpenOrder(s, core.Buy, 0); ok {
		t.Fatalf("should not place counter order before full fill")
	}

	if err := s.OnFill(context.Background(), core.Trade{
		OrderID: ord.ID,
		Symbol:  s.Symbol,
		Side:    core.Sell,
		Price:   ord.Price,
		Qty:     decimal.RequireFromString("0.6"),
		Status:  core.OrderFilled,
		Time:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("OnFill(final) error = %v", err)
	}

	if _, ok := s.openOrders[ord.ID]; ok {
		t.Fatalf("filled order should be removed")
	}
	if _, ok := findOpenOrder(s, core.Buy, 0); !ok {
		t.Fatalf("missing counter buy order at level 0 after full fill")
	}
}

func TestSpotDualOnFillClosedWithPartialDoesNotPlaceCounterOrder(t *testing.T) {
	s, _ := newSpotDualForTest(3, 1, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	ord, ok := findOpenOrder(s, core.Sell, 1)
	if !ok {
		t.Fatalf("missing sell order at level 1")
	}
	initialOpen := len(s.openOrders)

	if err := s.OnFill(context.Background(), core.Trade{
		OrderID: ord.ID,
		Symbol:  s.Symbol,
		Side:    core.Sell,
		Price:   ord.Price,
		Qty:     decimal.RequireFromString("0.4"),
		Status:  core.OrderCanceled,
		Time:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("OnFill(closed partial) error = %v", err)
	}

	if _, ok := s.openOrders[ord.ID]; ok {
		t.Fatalf("closed order should be removed from open orders")
	}
	if len(s.openOrders) != initialOpen-1 {
		t.Fatalf("open orders = %d, want %d", len(s.openOrders), initialOpen-1)
	}
	if _, ok := findOpenOrder(s, core.Buy, 0); ok {
		t.Fatalf("should not place counter buy order when order closes with partial fill")
	}
}

func TestSpotDualInitStopsWhenPriceAboveStopPrice(t *testing.T) {
	s, _ := newSpotDualForTest(3, 1, "10")
	s.StopPrice = decimal.NewFromInt(99)

	err := s.Init(context.Background(), decimal.NewFromInt(100))
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("Init() error = %v, want ErrStopped", err)
	}
	if !s.stopped {
		t.Fatalf("strategy should be stopped")
	}
	if len(s.openOrders) != 0 {
		t.Fatalf("open orders = %d, want 0", len(s.openOrders))
	}
}

func TestSpotDualOnFillStopsAndCancelsOpenOrders(t *testing.T) {
	s, exec := newSpotDualForTest(3, 1, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	s.StopPrice = decimal.NewFromInt(105)

	topSell, ok := findOpenOrder(s, core.Sell, s.maxLevel)
	if !ok {
		t.Fatalf("missing top sell order")
	}
	err := s.OnFill(context.Background(), core.Trade{
		OrderID: topSell.ID,
		Symbol:  s.Symbol,
		Side:    core.Sell,
		Price:   topSell.Price,
		Qty:     topSell.Qty,
		Time:    time.Now().UTC(),
	})
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("OnFill() error = %v, want ErrStopped", err)
	}
	if !s.stopped {
		t.Fatalf("strategy should be stopped")
	}
	if len(exec.canceled) != 3 {
		t.Fatalf("canceled orders = %d, want 3", len(exec.canceled))
	}
	if len(s.openOrders) != 0 {
		t.Fatalf("open orders = %d, want 0", len(s.openOrders))
	}
}

func TestSpotDualReconcileFillsOrderGaps(t *testing.T) {
	s, exec := newSpotDualForTest(2, 1, "10")
	s.LoadState(store.GridState{
		Symbol:      "BTCUSDT",
		Anchor:      decimal.NewFromInt(100),
		Ratio:       decimal.RequireFromString("1.1"),
		MinLevel:    -2,
		MaxLevel:    2,
		Initialized: true,
	})

	existing := core.Order{
		ID:        "existing-sell-1",
		Symbol:    "BTCUSDT",
		Side:      core.Sell,
		Type:      core.Limit,
		Price:     s.priceForLevel(1),
		Qty:       decimal.NewFromInt(1),
		GridIndex: 1,
	}
	if err := s.Reconcile(context.Background(), decimal.NewFromInt(100), []core.Order{existing}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	for _, tc := range []struct {
		side  core.Side
		level int
	}{
		{core.Sell, 1},
		{core.Sell, 2},
		{core.Buy, -1},
		{core.Buy, -2},
	} {
		if _, ok := findOpenOrder(s, tc.side, tc.level); !ok {
			t.Fatalf("missing order side=%s level=%d", tc.side, tc.level)
		}
	}
	if len(exec.placed) != 3 {
		t.Fatalf("placed orders = %d, want 3", len(exec.placed))
	}
}

func TestSpotDualReconcileKeepsZeroMinLevelAfterShiftedState(t *testing.T) {
	s, _ := newSpotDualForTest(3, 1, "10")
	s.LoadState(store.GridState{
		Symbol:      "BTCUSDT",
		Anchor:      decimal.NewFromInt(100),
		MinLevel:    0,
		MaxLevel:    6,
		Initialized: true,
	})

	if err := s.Reconcile(context.Background(), decimal.NewFromInt(100), nil); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if s.minLevel != 0 {
		t.Fatalf("minLevel = %d, want 0", s.minLevel)
	}
	for _, ord := range s.openOrders {
		if ord.Side == core.Buy && ord.GridIndex < 0 {
			t.Fatalf("unexpected negative buy level after reconcile: %d", ord.GridIndex)
		}
	}
}

func TestSpotDualOnTickRegimeChangeRebuildsOrders(t *testing.T) {
	s, exec := newSpotDualForTest(3, 1, "10")
	s.SetRegimeControl(RegimeControlConfig{
		Enabled:               true,
		Window:                5,
		EnterScore:            0.1,
		ExitScore:             0.05,
		EnterConfirm:          1,
		ExitConfirm:           1,
		TrendUpBuySpacingMult: 0.5,
	})
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initialBuy, ok := findOpenOrder(s, core.Buy, -1)
	if !ok {
		t.Fatalf("missing initial buy order at level -1")
	}
	initialPlaced := len(exec.placed)

	t0 := time.Unix(0, 0).UTC()
	changed := false
	for i, px := range []int64{101, 102, 103, 104, 105, 106} {
		err := s.OnTick(context.Background(), decimal.NewFromInt(px), t0.Add(time.Duration(i+1)*time.Minute))
		if err != nil {
			t.Fatalf("OnTick() error = %v", err)
		}
		if s.regimeState == RegimeTrendUp {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatalf("regime did not switch to trend_up")
	}
	if len(exec.canceled) == 0 {
		t.Fatalf("expected orders to be canceled during regime rebuild")
	}
	rebuiltBuy, ok := findOpenOrder(s, core.Buy, -1)
	if !ok {
		t.Fatalf("missing rebuilt buy order at level -1")
	}
	if rebuiltBuy.ID == initialBuy.ID {
		t.Fatalf("buy order at level -1 was not rebuilt")
	}
	if rebuiltBuy.Price.Cmp(initialBuy.Price) <= 0 {
		t.Fatalf("rebuilt buy price = %s, want > %s after trend_up spacing shrink", rebuiltBuy.Price, initialBuy.Price)
	}
	if len(exec.placed) <= initialPlaced {
		t.Fatalf("placed order count = %d, want > %d after rebuild", len(exec.placed), initialPlaced)
	}
}

func TestSpotDualOnTickRegimeRebuildResetsAnchorAndWindow(t *testing.T) {
	s, _ := newSpotDualForTest(3, 1, "10")
	s.SetRegimeControl(RegimeControlConfig{
		Enabled:               true,
		Window:                5,
		EnterScore:            0.1,
		ExitScore:             0.05,
		EnterConfirm:          1,
		ExitConfirm:           1,
		TrendUpBuySpacingMult: 0.5,
	})
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Simulate a previously shifted window around an outdated anchor.
	s.anchor = decimal.NewFromInt(90)
	s.minLevel = 0
	s.maxLevel = 6

	t0 := time.Unix(10, 0).UTC()
	changed := false
	changedPrice := decimal.Zero
	for i, px := range []int64{101, 102, 103, 104, 105, 106} {
		changedPrice = decimal.NewFromInt(px)
		err := s.OnTick(context.Background(), changedPrice, t0.Add(time.Duration(i+1)*time.Minute))
		if err != nil {
			t.Fatalf("OnTick() error = %v", err)
		}
		if s.regimeState == RegimeTrendUp {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatalf("regime did not switch to trend_up")
	}
	if !s.anchor.Equal(changedPrice) {
		t.Fatalf("anchor = %s, want %s after rebuild", s.anchor, changedPrice)
	}
	if s.minLevel != -3 || s.maxLevel != 1 {
		t.Fatalf("window = [%d,%d], want [-3,1] after rebuild", s.minLevel, s.maxLevel)
	}
	if hasAnyOpenOrderAtLevel(s, 2) {
		t.Fatalf("unexpected order above reset top level")
	}
}

func TestSpotDualOnTickRegimeRebuildDoesNotMarketBuyBase(t *testing.T) {
	s, exec := newSpotDualForTest(3, 1, "0")
	s.SetRegimeControl(RegimeControlConfig{
		Enabled:               true,
		Window:                5,
		EnterScore:            0.1,
		ExitScore:             0.05,
		EnterConfirm:          1,
		ExitConfirm:           1,
		TrendUpBuySpacingMult: 0.5,
	})
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	exec.balance.Base = decimal.Zero
	placedBefore := len(exec.placed)

	t0 := time.Unix(100, 0).UTC()
	changed := false
	for i, px := range []int64{101, 102, 103, 104, 105, 106} {
		err := s.OnTick(context.Background(), decimal.NewFromInt(px), t0.Add(time.Duration(i+1)*time.Minute))
		if err != nil {
			t.Fatalf("OnTick() error = %v", err)
		}
		if s.regimeState == RegimeTrendUp {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatalf("regime did not switch to trend_up")
	}

	marketBuyPlaced := 0
	for _, ord := range exec.placed[placedBefore:] {
		if ord.Type == core.Market && ord.Side == core.Buy {
			marketBuyPlaced++
		}
	}
	if marketBuyPlaced != 0 {
		t.Fatalf("market buy count during regime rebuild = %d, want 0", marketBuyPlaced)
	}
}

func TestSpotDualOnTickRegimeRebuildToleratesInsufficientQuote(t *testing.T) {
	exec := &insufficientDuringRebuildExecutor{
		fakeExecutor: fakeExecutor{
			balance: core.Balance{
				Base:  decimal.RequireFromString("10"),
				Quote: decimal.RequireFromString("1000000"),
			},
		},
	}
	s := NewSpotDual(
		"BTCUSDT",
		decimal.Zero,
		decimal.RequireFromString("1.1"),
		3,
		1,
		decimal.NewFromInt(1),
		1,
		core.Rules{},
		nil,
		exec,
	)
	s.SetRegimeControl(RegimeControlConfig{
		Enabled:               true,
		Window:                5,
		EnterScore:            0.1,
		ExitScore:             0.05,
		EnterConfirm:          1,
		ExitConfirm:           1,
		TrendUpBuySpacingMult: 0.5,
	})
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	exec.balance.Base = decimal.RequireFromString("10")
	exec.failBuyLimit = true

	t0 := time.Unix(200, 0).UTC()
	changed := false
	for i, px := range []int64{101, 102, 103, 104, 105, 106} {
		err := s.OnTick(context.Background(), decimal.NewFromInt(px), t0.Add(time.Duration(i+1)*time.Minute))
		if err != nil {
			t.Fatalf("OnTick() error = %v", err)
		}
		if s.regimeState == RegimeTrendUp {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatalf("regime did not switch to trend_up")
	}
	if len(s.openOrders) == 0 {
		t.Fatalf("expected partial rebuild to keep sell orders when buy placement is skipped")
	}
}

func TestSpotDualShiftUpTriggersMarketBuyWhenBaseInsufficient(t *testing.T) {
	s, exec := newSpotDualForTest(3, 1, "2")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	exec.balance.Base = decimal.Zero
	placedBefore := len(exec.placed)
	topSell, ok := findOpenOrder(s, core.Sell, s.maxLevel)
	if !ok {
		t.Fatalf("missing top sell order")
	}

	if err := s.OnFill(context.Background(), core.Trade{
		OrderID: topSell.ID,
		Symbol:  s.Symbol,
		Side:    core.Sell,
		Price:   topSell.Price,
		Qty:     topSell.Qty,
		Time:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("OnFill() error = %v", err)
	}

	marketBuyCount := 0
	for _, ord := range exec.placed[placedBefore:] {
		if ord.Type == core.Market && ord.Side == core.Buy {
			marketBuyCount++
		}
	}
	if marketBuyCount != 1 {
		t.Fatalf("shiftUp market buy count = %d, want 1", marketBuyCount)
	}
}

func TestSpotDualPlaceLimitSkipsInsufficientBalance(t *testing.T) {
	exec := &insufficientDuringRebuildExecutor{
		fakeExecutor: fakeExecutor{
			balance: core.Balance{
				Base:  decimal.RequireFromString("10"),
				Quote: decimal.RequireFromString("10"),
			},
		},
		failBuyLimit: true,
	}
	s := NewSpotDual(
		"BTCUSDT",
		decimal.Zero,
		decimal.RequireFromString("1.1"),
		3,
		1,
		decimal.NewFromInt(1),
		1,
		core.Rules{},
		nil,
		exec,
	)
	s.anchor = decimal.NewFromInt(100)
	s.minLevel = -3
	s.maxLevel = 2

	if err := s.placeLimit(context.Background(), core.Buy, -1); err != nil {
		t.Fatalf("placeLimit() error = %v", err)
	}
	if len(s.openOrders) != 0 {
		t.Fatalf("open orders = %d, want 0 when insufficient balance", len(s.openOrders))
	}
}
