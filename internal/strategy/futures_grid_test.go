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
		return core.Order{}, fmt.Errorf("%w: quote balance", core.ErrInsufficientBalance)
	}
	if f.failBuyLimit && order.Type == core.Limit && order.Side == core.Buy {
		return core.Order{}, fmt.Errorf("%w: quote balance", core.ErrInsufficientBalance)
	}
	return f.fakeExecutor.PlaceOrder(ctx, order)
}

func newFuturesGridForTest(levels, shift int, baseBalance string) (*FuturesGrid, *fakeExecutor) {
	exec := &fakeExecutor{
		balance: core.Balance{
			Base:  decimal.RequireFromString(baseBalance),
			Quote: decimal.NewFromInt(1_000_000),
		},
	}
	s := NewFuturesGrid(
		"BTCUSDT",
		ContractModeDual,
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

func findOpenOrder(s *FuturesGrid, side core.Side, idx int) (core.Order, bool) {
	for _, ord := range s.openOrders {
		if ord.Side == side && ord.GridIndex == idx {
			return ord, true
		}
	}
	return core.Order{}, false
}

func hasAnyOpenOrderAtLevel(s *FuturesGrid, idx int) bool {
	for _, ord := range s.openOrders {
		if ord.GridIndex == idx {
			return true
		}
	}
	return false
}

func TestFuturesGridInitPlacesInitialOrders(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if !s.initialized {
		t.Fatalf("strategy should be initialized")
	}
	if s.maxLevel != 3 {
		t.Fatalf("maxLevel = %d, want 3", s.maxLevel)
	}
	if s.minLevel != -3 {
		t.Fatalf("minLevel = %d, want -3", s.minLevel)
	}
	if len(s.openOrders) != 6 {
		t.Fatalf("open orders = %d, want 6", len(s.openOrders))
	}
	for _, level := range []int{1, 2, 3} {
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

func TestFuturesGridPriceForLevelUsesBuyAndSellRatios(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
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

func TestFuturesGridPlaceLimitKeepsBuyAndSellQtyEqual(t *testing.T) {
	exec := &fakeExecutor{
		balance: core.Balance{
			Base:  decimal.RequireFromString("100"),
			Quote: decimal.RequireFromString("1000000"),
		},
	}
	s := NewFuturesGrid(
		"BTCUSDT",
		ContractModeDual,
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

	if !sell.Qty.Equal(decimal.RequireFromString("2")) {
		t.Fatalf("sell qty = %s, want 2", sell.Qty)
	}
	if !buy.Qty.Equal(decimal.RequireFromString("2")) {
		t.Fatalf("buy qty = %s, want 2", buy.Qty)
	}
}

func TestFuturesGridInitDoesNotRequireBaseBootstrapMarketBuy(t *testing.T) {
	s, exec := newFuturesGridForTest(3, 1, "0")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	marketBuyCount := 0
	for _, ord := range exec.placed {
		if ord.Type == core.Market && ord.Side == core.Buy {
			marketBuyCount++
		}
	}
	if marketBuyCount != 0 {
		t.Fatalf("market buy count = %d, want 0", marketBuyCount)
	}
}

func TestFuturesGridOnFillSellAtTopShiftsUp(t *testing.T) {
	s, exec := newFuturesGridForTest(3, 1, "10")
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
	if _, ok := findOpenOrder(s, core.Buy, oldMax); ok {
		t.Fatalf("unexpected pre-placed reduce-only buy at level %d", oldMax)
	}
}

func TestFuturesGridOnFillBuyAtBottomExtendsDown(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
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

	wantMin := oldMin - s.shiftLevels()
	if s.minLevel != wantMin {
		t.Fatalf("minLevel = %d, want %d", s.minLevel, wantMin)
	}
	for level := oldMin - 1; level >= wantMin; level-- {
		if _, ok := findOpenOrder(s, core.Buy, level); !ok {
			t.Fatalf("missing extended buy at level %d", level)
		}
	}
}

func TestFuturesGridOnFillBuyAtBottomExtendsDownWithRatioQtyMultiple(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
	s.SetRatioQtyMultiple(decimal.RequireFromString("1.2"))
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

	wantQty := decimal.RequireFromString("1.2")
	wantMin := oldMin - s.shiftLevels()
	for level := oldMin - 1; level >= wantMin; level-- {
		buy, ok := findOpenOrder(s, core.Buy, level)
		if !ok {
			t.Fatalf("missing extended buy at level %d", level)
		}
		if !buy.Qty.Equal(wantQty) {
			t.Fatalf("extended buy qty at level %d = %s, want %s", level, buy.Qty, wantQty)
		}
	}

	existingBuy, ok := findOpenOrder(s, core.Buy, oldMin+1)
	if !ok {
		t.Fatalf("missing existing buy order at level %d", oldMin+1)
	}
	if !existingBuy.Qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("existing buy qty = %s, want 1", existingBuy.Qty)
	}
}

func TestFuturesGridLongModeOnFillBuyAtBottomExtendsDown(t *testing.T) {
	exec := &fakeExecutor{
		balance: core.Balance{
			Base:  decimal.RequireFromString("10"),
			Quote: decimal.NewFromInt(1_000_000),
		},
	}
	s := NewFuturesGrid(
		"BTCUSDT",
		ContractModeLong,
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
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if s.maxLevel != 0 || s.minLevel != -3 {
		t.Fatalf("window = [%d,%d], want [-3,0]", s.minLevel, s.maxLevel)
	}
	for _, level := range []int{-1, -2, -3} {
		if _, ok := findOpenOrder(s, core.Buy, level); !ok {
			t.Fatalf("missing long-mode buy order at level %d", level)
		}
	}

	bottomBuy, ok := findOpenOrder(s, core.Buy, s.minLevel)
	if !ok {
		t.Fatalf("missing long-mode bottom buy")
	}
	oldMin := s.minLevel
	if err := s.OnFill(context.Background(), core.Trade{
		OrderID: bottomBuy.ID,
		Symbol:  s.Symbol,
		Side:    core.Buy,
		Price:   bottomBuy.Price,
		Qty:     bottomBuy.Qty,
		Time:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("OnFill() error = %v", err)
	}

	if s.maxLevel != 0 {
		t.Fatalf("maxLevel = %d, want 0 (long mode should not shift up)", s.maxLevel)
	}
	wantMin := oldMin - s.shiftLevels()
	if s.minLevel != wantMin {
		t.Fatalf("minLevel = %d, want %d", s.minLevel, wantMin)
	}
	for level := oldMin - 1; level >= wantMin; level-- {
		if _, ok := findOpenOrder(s, core.Buy, level); !ok {
			t.Fatalf("missing extended long-mode buy at level %d", level)
		}
	}
	if _, ok := findOpenOrder(s, core.Sell, oldMin+1); !ok {
		t.Fatalf("missing long-mode counter sell at level %d", oldMin+1)
	}
	if len(exec.canceled) != 0 {
		t.Fatalf("canceled orders = %d, want 0", len(exec.canceled))
	}
}

func TestFuturesGridShortModeOnFillSellAtTopExtendsUp(t *testing.T) {
	exec := &fakeExecutor{
		balance: core.Balance{
			Base:  decimal.RequireFromString("10"),
			Quote: decimal.NewFromInt(1_000_000),
		},
	}
	s := NewFuturesGrid(
		"BTCUSDT",
		ContractModeShort,
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
	s.SetRatioQtyMultiple(decimal.RequireFromString("1.2"))
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if s.minLevel != 0 || s.maxLevel != 3 {
		t.Fatalf("window = [%d,%d], want [0,3]", s.minLevel, s.maxLevel)
	}
	for _, level := range []int{1, 2, 3} {
		if _, ok := findOpenOrder(s, core.Sell, level); !ok {
			t.Fatalf("missing short-mode sell order at level %d", level)
		}
	}

	topSell, ok := findOpenOrder(s, core.Sell, s.maxLevel)
	if !ok {
		t.Fatalf("missing short-mode top sell")
	}
	oldMax := s.maxLevel
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

	if s.minLevel != 0 {
		t.Fatalf("minLevel = %d, want 0 (short mode should not shift down)", s.minLevel)
	}
	wantMax := oldMax + s.shiftLevels()
	if s.maxLevel != wantMax {
		t.Fatalf("maxLevel = %d, want %d", s.maxLevel, wantMax)
	}
	if _, ok := findOpenOrder(s, core.Buy, oldMax-1); !ok {
		t.Fatalf("missing short-mode counter buy at level %d", oldMax-1)
	}
	for level := oldMax + 1; level <= wantMax; level++ {
		ord, ok := findOpenOrder(s, core.Sell, level)
		if !ok {
			t.Fatalf("missing extended short-mode sell at level %d", level)
		}
		if !ord.Qty.Equal(decimal.NewFromInt(1)) {
			t.Fatalf("extended short-mode sell qty at level %d = %s, want 1", level, ord.Qty)
		}
	}
	if len(exec.canceled) != 0 {
		t.Fatalf("canceled orders = %d, want 0", len(exec.canceled))
	}
}

func TestFuturesGridDownShiftDefenseRaisesBuyRatioOnEveryShift(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
	s.Ratio = decimal.RequireFromString("1.1")
	s.baseBuyRatio = decimal.RequireFromString("1.1")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	firstBottom, ok := findOpenOrder(s, core.Buy, s.minLevel)
	if !ok {
		t.Fatalf("missing first bottom buy")
	}
	t0 := time.Now().UTC()
	if err := s.OnFill(context.Background(), core.Trade{
		OrderID: firstBottom.ID,
		Symbol:  s.Symbol,
		Side:    core.Buy,
		Price:   firstBottom.Price,
		Qty:     firstBottom.Qty,
		Time:    t0,
	}); err != nil {
		t.Fatalf("OnFill(first) error = %v", err)
	}
	if !s.Ratio.Equal(decimal.RequireFromString("1.102")) {
		t.Fatalf("ratio after first down shift = %s, want 1.102", s.Ratio)
	}

	secondBottom, ok := findOpenOrder(s, core.Buy, s.minLevel)
	if !ok {
		t.Fatalf("missing second bottom buy")
	}
	if err := s.OnFill(context.Background(), core.Trade{
		OrderID: secondBottom.ID,
		Symbol:  s.Symbol,
		Side:    core.Buy,
		Price:   secondBottom.Price,
		Qty:     secondBottom.Qty,
		Time:    t0.Add(time.Minute),
	}); err != nil {
		t.Fatalf("OnFill(second) error = %v", err)
	}
	if !s.Ratio.Equal(decimal.RequireFromString("1.104")) {
		t.Fatalf("ratio after second down shift = %s, want 1.104", s.Ratio)
	}
}

func TestFuturesGridDownShiftDefenseCanDisableRatioIncrement(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
	s.Ratio = decimal.RequireFromString("1.1")
	s.baseBuyRatio = decimal.RequireFromString("1.1")
	s.SetRatioStep(decimal.Zero)
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	bottom, ok := findOpenOrder(s, core.Buy, s.minLevel)
	if !ok {
		t.Fatalf("missing bottom buy")
	}
	if err := s.OnFill(context.Background(), core.Trade{
		OrderID: bottom.ID,
		Symbol:  s.Symbol,
		Side:    core.Buy,
		Price:   bottom.Price,
		Qty:     bottom.Qty,
		Time:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("OnFill() error = %v", err)
	}
	if !s.Ratio.Equal(decimal.RequireFromString("1.1")) {
		t.Fatalf("ratio with ratio_step=0 = %s, want 1.1", s.Ratio)
	}

	secondBottom, ok := findOpenOrder(s, core.Buy, s.minLevel)
	if !ok {
		t.Fatalf("missing second bottom buy")
	}
	if err := s.OnFill(context.Background(), core.Trade{
		OrderID: secondBottom.ID,
		Symbol:  s.Symbol,
		Side:    core.Buy,
		Price:   secondBottom.Price,
		Qty:     secondBottom.Qty,
		Time:    time.Now().UTC().Add(time.Minute),
	}); err != nil {
		t.Fatalf("OnFill(second) error = %v", err)
	}
	if !s.Ratio.Equal(decimal.RequireFromString("1.1")) {
		t.Fatalf("ratio after second shift with ratio_step=0 = %s, want 1.1", s.Ratio)
	}
}

func TestFuturesGridSetRatioStepIgnoresNegativeValue(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
	s.Ratio = decimal.RequireFromString("1.1")
	s.baseBuyRatio = decimal.RequireFromString("1.1")
	s.SetRatioStep(decimal.RequireFromString("0.003"))
	s.SetRatioStep(decimal.RequireFromString("-0.001"))

	s.onDownShiftTriggered(decimal.RequireFromString("99"), time.Now().UTC())
	if !s.Ratio.Equal(decimal.RequireFromString("1.103")) {
		t.Fatalf("ratio after negative ratio_step override = %s, want 1.103", s.Ratio)
	}
}

func TestFuturesGridSetRatioQtyMultipleIgnoresNonPositiveValue(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
	s.SetRatioQtyMultiple(decimal.RequireFromString("1.2"))
	s.SetRatioQtyMultiple(decimal.Zero)
	s.SetRatioQtyMultiple(decimal.RequireFromString("-1"))
	if !s.RatioQtyMultiple.Equal(decimal.RequireFromString("1.2")) {
		t.Fatalf("ratio_qty_multiple = %s, want 1.2", s.RatioQtyMultiple.String())
	}
}

func TestFuturesGridDownShiftDefenseDoesNotRestoreOnTickWithoutShiftUp(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
	s.initialized = true
	s.anchor = decimal.NewFromInt(100)
	s.baseBuyRatio = decimal.RequireFromString("1.1")
	s.Ratio = decimal.RequireFromString("1.103")
	s.SellRatio = decimal.RequireFromString("1.1")
	s.maxLevel = 1
	s.minLevel = -3
	now := time.Now().UTC()
	s.lastDownShiftAt = now.Add(-7 * 24 * time.Hour)

	if err := s.OnTick(context.Background(), decimal.NewFromInt(100), now); err != nil {
		t.Fatalf("OnTick() error = %v", err)
	}
	if !s.Ratio.Equal(decimal.RequireFromString("1.103")) {
		t.Fatalf("ratio after tick = %s, want 1.103", s.Ratio)
	}
	if !s.lastDownShiftAt.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Fatalf("lastDownShiftAt changed unexpectedly")
	}
}

func TestFuturesGridDownShiftDefenseRestoresBuyRatioOnShiftUp(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
	s.baseBuyRatio = decimal.RequireFromString("1.1")
	s.Ratio = decimal.RequireFromString("1.103")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	topSell, ok := findOpenOrder(s, core.Sell, s.maxLevel)
	if !ok {
		t.Fatalf("missing top sell order")
	}
	now := time.Now().UTC()
	if err := s.OnFill(context.Background(), core.Trade{
		OrderID: topSell.ID,
		Symbol:  s.Symbol,
		Side:    core.Sell,
		Price:   topSell.Price,
		Qty:     topSell.Qty,
		Time:    now,
	}); err != nil {
		t.Fatalf("OnFill(top sell) error = %v", err)
	}
	if !s.Ratio.Equal(decimal.RequireFromString("1.1")) {
		t.Fatalf("ratio after shift up restore = %s, want 1.1", s.Ratio)
	}
	if !s.lastDownShiftPrice.Equal(topSell.Price) {
		t.Fatalf("lastDownShiftPrice after restore = %s, want %s", s.lastDownShiftPrice, topSell.Price)
	}
	if s.lastDownShiftAt.IsZero() {
		t.Fatalf("lastDownShiftAt should record restore time")
	}
}

func TestFuturesGridDownShiftDefenseAfterRestoreRaisesOnNextShift(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
	s.baseBuyRatio = decimal.RequireFromString("1.1")
	s.Ratio = decimal.RequireFromString("1.103")
	now := time.Now().UTC()

	s.restoreBuyRatioOnShiftUp(decimal.RequireFromString("100"), now)
	if !s.Ratio.Equal(decimal.RequireFromString("1.1")) {
		t.Fatalf("ratio after restore = %s, want 1.1", s.Ratio)
	}
	if !s.lastDownShiftPrice.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("lastDownShiftPrice after restore = %s, want 100", s.lastDownShiftPrice)
	}

	s.onDownShiftTriggered(decimal.RequireFromString("99"), now.Add(time.Minute))
	if !s.Ratio.Equal(decimal.RequireFromString("1.102")) {
		t.Fatalf("ratio after new low trigger = %s, want 1.102", s.Ratio)
	}
}

func TestFuturesGridShiftUpCancelFailureKeepsFailedOrderTracked(t *testing.T) {
	s, exec := newFuturesGridForTest(3, 1, "10")
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

func TestFuturesGridOnFillPartialThenFilled(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
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

func TestFuturesGridOnFillClosedWithPartialDoesNotPlaceCounterOrder(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
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

func TestFuturesGridInitStopsWhenPriceAboveStopPrice(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
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

func TestFuturesGridOnFillStopsAndCancelsOpenOrders(t *testing.T) {
	s, exec := newFuturesGridForTest(3, 1, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	s.StopPrice = decimal.NewFromInt(105)
	expectedCancels := len(s.openOrders) - 1

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
	if len(exec.canceled) != expectedCancels {
		t.Fatalf("canceled orders = %d, want %d", len(exec.canceled), expectedCancels)
	}
	if len(s.openOrders) != 0 {
		t.Fatalf("open orders = %d, want 0", len(s.openOrders))
	}
}

func TestFuturesGridStopNowCancelsAllOrders(t *testing.T) {
	s, exec := newFuturesGridForTest(3, 2, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	totalOpen := len(s.openOrders)
	for id, ord := range s.openOrders {
		if id == "" || ord.Symbol == "" {
			t.Fatalf("unexpected empty order metadata before stop")
		}
	}

	s.StopPrice = decimal.NewFromInt(105)
	err := s.OnTick(context.Background(), decimal.NewFromInt(110), time.Now().UTC())
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("OnTick() error = %v, want ErrStopped", err)
	}
	if !s.stopped {
		t.Fatalf("strategy should be stopped")
	}
	if len(exec.canceled) != totalOpen {
		t.Fatalf("canceled orders = %d, want %d", len(exec.canceled), totalOpen)
	}
	if len(s.openOrders) != 0 {
		t.Fatalf("open orders after stop = %d, want 0", len(s.openOrders))
	}
}

func TestFuturesGridStopNowIgnoresCancelBuyErrors(t *testing.T) {
	s, exec := newFuturesGridForTest(3, 2, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	failedID := ""
	for id, ord := range s.openOrders {
		if ord.Side == core.Buy {
			failedID = id
			break
		}
	}
	if failedID == "" {
		t.Fatalf("missing buy order for failure case")
	}
	exec.cancelErrByID = map[string]error{
		failedID: errors.New("order not found"),
	}

	s.StopPrice = decimal.NewFromInt(105)
	err := s.OnTick(context.Background(), decimal.NewFromInt(110), time.Now().UTC())
	if err != nil {
		t.Fatalf("OnTick() error = %v, want nil while buy cancel is still pending", err)
	}
	if !s.stopped {
		t.Fatalf("strategy should be stopped")
	}
	if _, ok := s.openOrders[failedID]; !ok {
		t.Fatalf("failed cancel buy order should remain in openOrders")
	}
}

func TestFuturesGridReconcileStoppedRetriesCancelBuyOrdersUntilCleared(t *testing.T) {
	s, exec := newFuturesGridForTest(3, 2, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	failedID := ""
	for id, ord := range s.openOrders {
		if ord.Side == core.Buy {
			failedID = id
			break
		}
	}
	if failedID == "" {
		t.Fatalf("missing buy order for failure case")
	}
	exec.cancelErrByID = map[string]error{
		failedID: errors.New("order not found"),
	}

	s.StopPrice = decimal.NewFromInt(105)
	if err := s.OnTick(context.Background(), decimal.NewFromInt(110), time.Now().UTC()); err != nil {
		t.Fatalf("OnTick() error = %v, want nil while buy cancel is still pending", err)
	}
	if _, ok := s.openOrders[failedID]; !ok {
		t.Fatalf("failed cancel buy order should remain in openOrders")
	}

	exec.cancelErrByID = nil
	err := s.Reconcile(context.Background(), decimal.NewFromInt(110), s.snapshotOrders())
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("Reconcile() error = %v, want ErrStopped once all buy orders are cleared", err)
	}
	if _, ok := s.openOrders[failedID]; ok {
		t.Fatalf("failed cancel buy order should be removed after successful reconcile retry")
	}
	for _, ord := range s.openOrders {
		if ord.Side == core.Buy {
			t.Fatalf("unexpected buy order after stopped reconcile: %+v", ord)
		}
	}
}

func TestFuturesGridReconcileStoppedReturnsErrStoppedWhenNoBuyOrders(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 2, "10")
	if err := s.Init(context.Background(), decimal.NewFromInt(100)); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	for id, ord := range s.openOrders {
		if ord.Side == core.Buy {
			delete(s.openOrders, id)
		}
	}
	s.stopped = true
	s.initialized = false

	err := s.Reconcile(context.Background(), decimal.NewFromInt(110), s.snapshotOrders())
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("Reconcile() error = %v, want ErrStopped", err)
	}
}

func TestFuturesGridReconcileFillsOrderGaps(t *testing.T) {
	s, exec := newFuturesGridForTest(2, 1, "10")
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

func TestFuturesGridReconcileKeepsZeroMinLevelAfterShiftedState(t *testing.T) {
	s, _ := newFuturesGridForTest(3, 1, "10")
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

func TestFuturesGridReconcileCancelsDuplicateLevelOrders(t *testing.T) {
	s, exec := newFuturesGridForTest(2, 1, "10")
	s.LoadState(store.GridState{
		Symbol:      "BTCUSDT",
		Anchor:      decimal.NewFromInt(100),
		Ratio:       decimal.RequireFromString("1.1"),
		MinLevel:    -2,
		MaxLevel:    2,
		Initialized: true,
	})

	open := []core.Order{
		{
			ID:     "dup-sell-a",
			Symbol: "BTCUSDT",
			Side:   core.Sell,
			Type:   core.Limit,
			Price:  s.priceForLevel(1),
			Qty:    decimal.NewFromInt(1),
		},
		{
			ID:     "dup-sell-b",
			Symbol: "BTCUSDT",
			Side:   core.Sell,
			Type:   core.Limit,
			Price:  s.priceForLevel(1),
			Qty:    decimal.NewFromInt(1),
		},
		{
			ID:     "sell-2",
			Symbol: "BTCUSDT",
			Side:   core.Sell,
			Type:   core.Limit,
			Price:  s.priceForLevel(2),
			Qty:    decimal.NewFromInt(1),
		},
		{
			ID:     "buy-1",
			Symbol: "BTCUSDT",
			Side:   core.Buy,
			Type:   core.Limit,
			Price:  s.priceForLevel(-1),
			Qty:    decimal.NewFromInt(1),
		},
		{
			ID:     "buy-2",
			Symbol: "BTCUSDT",
			Side:   core.Buy,
			Type:   core.Limit,
			Price:  s.priceForLevel(-2),
			Qty:    decimal.NewFromInt(1),
		},
	}

	if err := s.Reconcile(context.Background(), decimal.NewFromInt(100), open); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if len(exec.canceled) != 1 {
		t.Fatalf("canceled orders = %d, want 1", len(exec.canceled))
	}
	if exec.canceled[0] != "dup-sell-b" {
		t.Fatalf("canceled order = %s, want dup-sell-b", exec.canceled[0])
	}

	if _, ok := s.openOrders["dup-sell-a"]; !ok {
		t.Fatalf("kept duplicate leader dup-sell-a should remain")
	}
	if _, ok := s.openOrders["dup-sell-b"]; ok {
		t.Fatalf("duplicate order dup-sell-b should be removed")
	}
	if len(s.openOrders) != 4 {
		t.Fatalf("open orders = %d, want 4 after dedupe", len(s.openOrders))
	}
	if len(exec.placed) != 0 {
		t.Fatalf("placed orders = %d, want 0", len(exec.placed))
	}
}

func TestFuturesGridReconcileKeepsCrossSideOrdersAndRefillsBaseGrid(t *testing.T) {
	s, exec := newFuturesGridForTest(2, 1, "10")
	s.LoadState(store.GridState{
		Symbol:      "BTCUSDT",
		Anchor:      decimal.NewFromInt(100),
		Ratio:       decimal.RequireFromString("1.1"),
		MinLevel:    -2,
		MaxLevel:    2,
		Initialized: true,
	})

	open := []core.Order{
		{
			ID:     "conflict-buy-1",
			Symbol: "BTCUSDT",
			Side:   core.Buy,
			Type:   core.Limit,
			Price:  s.priceForLevel(1),
			Qty:    decimal.NewFromInt(1),
		},
		{
			ID:     "sell-2",
			Symbol: "BTCUSDT",
			Side:   core.Sell,
			Type:   core.Limit,
			Price:  s.priceForLevel(2),
			Qty:    decimal.NewFromInt(1),
		},
		{
			ID:     "conflict-sell-minus-1",
			Symbol: "BTCUSDT",
			Side:   core.Sell,
			Type:   core.Limit,
			Price:  s.priceForLevel(-1),
			Qty:    decimal.NewFromInt(1),
		},
		{
			ID:     "buy-minus-2",
			Symbol: "BTCUSDT",
			Side:   core.Buy,
			Type:   core.Limit,
			Price:  s.priceForLevel(-2),
			Qty:    decimal.NewFromInt(1),
		},
	}

	if err := s.Reconcile(context.Background(), decimal.NewFromInt(100), open); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if len(exec.canceled) != 0 {
		t.Fatalf("canceled orders = %d, want 0", len(exec.canceled))
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
	if _, ok := s.openOrders["conflict-buy-1"]; !ok {
		t.Fatalf("existing cross-side order conflict-buy-1 should be kept")
	}
	if _, ok := s.openOrders["conflict-sell-minus-1"]; !ok {
		t.Fatalf("existing cross-side order conflict-sell-minus-1 should be kept")
	}
}

func TestFuturesGridShiftUpDoesNotMarketBuyBase(t *testing.T) {
	s, exec := newFuturesGridForTest(3, 1, "2")
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
	if marketBuyCount != 0 {
		t.Fatalf("shiftUp market buy count = %d, want 0", marketBuyCount)
	}
}

func TestFuturesGridPlaceLimitSkipsInsufficientBalance(t *testing.T) {
	exec := &insufficientDuringRebuildExecutor{
		fakeExecutor: fakeExecutor{
			balance: core.Balance{
				Base:  decimal.RequireFromString("10"),
				Quote: decimal.RequireFromString("10"),
			},
		},
		failBuyLimit: true,
	}
	s := NewFuturesGrid(
		"BTCUSDT",
		ContractModeDual,
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

func TestFuturesGridReconcileRefillsMissingSellLevelsWithoutMarketBuy(t *testing.T) {
	exec := &fakeExecutor{
		balance: core.Balance{
			Base:  decimal.Zero,
			Quote: decimal.RequireFromString("1000000"),
		},
	}
	s := NewFuturesGrid(
		"BTCUSDT",
		ContractModeDual,
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
	s.LoadState(store.GridState{
		Symbol:      "BTCUSDT",
		Anchor:      decimal.NewFromInt(100),
		Ratio:       decimal.RequireFromString("1.1"),
		MinLevel:    -3,
		MaxLevel:    2,
		Initialized: true,
	})
	open := []core.Order{
		{
			ID:     "buy-1",
			Symbol: "BTCUSDT",
			Side:   core.Buy,
			Type:   core.Limit,
			Price:  s.priceForLevel(-1),
			Qty:    decimal.NewFromInt(1),
		},
		{
			ID:     "buy-2",
			Symbol: "BTCUSDT",
			Side:   core.Buy,
			Type:   core.Limit,
			Price:  s.priceForLevel(-2),
			Qty:    decimal.NewFromInt(1),
		},
		{
			ID:     "buy-3",
			Symbol: "BTCUSDT",
			Side:   core.Buy,
			Type:   core.Limit,
			Price:  s.priceForLevel(-3),
			Qty:    decimal.NewFromInt(1),
		},
	}

	if err := s.Reconcile(context.Background(), decimal.NewFromInt(100), open); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !s.initialized {
		t.Fatalf("strategy should be initialized after complete reconcile")
	}
	if _, ok := findOpenOrder(s, core.Sell, 1); !ok {
		t.Fatalf("missing sell order at level 1 after reconcile")
	}
	if _, ok := findOpenOrder(s, core.Sell, 2); !ok {
		t.Fatalf("missing sell order at level 2 after reconcile")
	}

	marketBuyCount := 0
	for _, ord := range exec.placed {
		if ord.Type == core.Market && ord.Side == core.Buy {
			marketBuyCount++
			if !ord.Qty.Equal(decimal.NewFromInt(1)) {
				t.Fatalf("reconcile market buy qty = %s, want 1", ord.Qty)
			}
		}
	}
	if marketBuyCount != 0 {
		t.Fatalf("reconcile market buy count = %d, want 0", marketBuyCount)
	}
}

func TestFuturesGridReconcileDoesNotDependOnMarketBuyPath(t *testing.T) {
	exec := &insufficientDuringRebuildExecutor{
		fakeExecutor: fakeExecutor{
			balance: core.Balance{
				Base:  decimal.Zero,
				Quote: decimal.RequireFromString("1000000"),
			},
		},
		failMarketBuy: true,
	}
	s := NewFuturesGrid(
		"BTCUSDT",
		ContractModeDual,
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
	s.LoadState(store.GridState{
		Symbol:      "BTCUSDT",
		Anchor:      decimal.NewFromInt(100),
		Ratio:       decimal.RequireFromString("1.1"),
		MinLevel:    -3,
		MaxLevel:    2,
		Initialized: true,
	})
	open := []core.Order{
		{
			ID:     "buy-1",
			Symbol: "BTCUSDT",
			Side:   core.Buy,
			Type:   core.Limit,
			Price:  s.priceForLevel(-1),
			Qty:    decimal.NewFromInt(1),
		},
		{
			ID:     "buy-2",
			Symbol: "BTCUSDT",
			Side:   core.Buy,
			Type:   core.Limit,
			Price:  s.priceForLevel(-2),
			Qty:    decimal.NewFromInt(1),
		},
		{
			ID:     "buy-3",
			Symbol: "BTCUSDT",
			Side:   core.Buy,
			Type:   core.Limit,
			Price:  s.priceForLevel(-3),
			Qty:    decimal.NewFromInt(1),
		},
	}

	err := s.Reconcile(context.Background(), decimal.NewFromInt(100), open)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if !s.initialized {
		t.Fatalf("strategy should be initialized")
	}
	if _, ok := findOpenOrder(s, core.Sell, 1); !ok {
		t.Fatalf("missing sell order at level 1 after reconcile")
	}
}
