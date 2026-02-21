package strategy

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"grid-trading/internal/alert"
	"grid-trading/internal/core"
	"grid-trading/internal/store"
)

const defaultFuturesRatioStep = "0.002"
const defaultFuturesRatioQtyMultiple = "1"

type ContractMode string

const (
	ContractModeDual  ContractMode = "dual"
	ContractModeLong  ContractMode = "long"
	ContractModeShort ContractMode = "short"
)

// OrderExecutor keeps Balances for compatibility with existing fakes/tests,
// even though this strategy does not depend on account asset buckets.
type OrderExecutor interface {
	PlaceOrder(ctx context.Context, order core.Order) (core.Order, error)
	CancelOrder(ctx context.Context, symbol, orderID string) error
	Balances(ctx context.Context) (core.Balance, error)
}

type FuturesGrid struct {
	Symbol           string
	StopPrice        decimal.Decimal
	Ratio            decimal.Decimal
	SellRatio        decimal.Decimal
	RatioStep        decimal.Decimal
	RatioQtyMultiple decimal.Decimal
	Levels           int
	Shift            int
	Qty              decimal.Decimal
	ContractMode     ContractMode

	minQtyMultiple int64
	rules          core.Rules
	executor       OrderExecutor
	openOrders     map[string]core.Order
	initialized    bool
	store          store.Persister
	alerter        alert.Alerter

	anchor      decimal.Decimal
	minLevel    int
	maxLevel    int
	stopped     bool
	ignoreFills map[string]struct{}

	baseBuyRatio       decimal.Decimal
	lastDownShiftPrice decimal.Decimal
	lastDownShiftAt    time.Time
}

func NewFuturesGrid(symbol string, mode ContractMode, stopPrice, ratio decimal.Decimal, levels, shift int, qty decimal.Decimal, minQtyMultiple int64, rules core.Rules, st store.Persister, executor OrderExecutor) *FuturesGrid {
	return &FuturesGrid{
		Symbol:           symbol,
		StopPrice:        stopPrice,
		Ratio:            ratio,
		SellRatio:        ratio,
		RatioStep:        decimal.RequireFromString(defaultFuturesRatioStep),
		RatioQtyMultiple: decimal.RequireFromString(defaultFuturesRatioQtyMultiple),
		Levels:           levels,
		Shift:            shift,
		Qty:              qty,
		ContractMode:     normalizeContractMode(mode),
		minQtyMultiple:   minQtyMultiple,
		rules:            rules,
		executor:         executor,
		openOrders:       make(map[string]core.Order),
		store:            st,
		ignoreFills:      make(map[string]struct{}),
		baseBuyRatio:     ratio,
	}
}

func (s *FuturesGrid) mode() ContractMode {
	s.ContractMode = normalizeContractMode(s.ContractMode)
	return s.ContractMode
}

func normalizeContractMode(mode ContractMode) ContractMode {
	m := ContractMode(strings.ToLower(strings.TrimSpace(string(mode))))
	switch m {
	case ContractModeLong, ContractModeShort:
		return m
	default:
		return ContractModeDual
	}
}

func (s *FuturesGrid) LoadState(state store.GridState) {
	if state.Symbol != "" && state.Symbol != s.Symbol {
		return
	}
	if state.StopPrice.Cmp(decimal.Zero) > 0 {
		s.StopPrice = state.StopPrice
	}
	if state.Ratio.Cmp(decimal.NewFromInt(1)) > 0 {
		s.Ratio = state.Ratio
	}
	if state.BaseRatio.Cmp(decimal.NewFromInt(1)) > 0 {
		s.baseBuyRatio = state.BaseRatio
	}
	if state.SellRatio.Cmp(decimal.NewFromInt(1)) > 0 {
		s.SellRatio = state.SellRatio
	}
	if state.Anchor.Cmp(decimal.Zero) > 0 {
		s.anchor = state.Anchor
	}
	if state.MinLevel != 0 {
		s.minLevel = state.MinLevel
	}
	if state.MaxLevel != 0 {
		s.maxLevel = state.MaxLevel
	}
	if state.Initialized {
		s.initialized = true
	}
	if state.Stopped {
		s.stopped = true
	}
	if state.LastDownShiftPrice.Cmp(decimal.Zero) > 0 {
		s.lastDownShiftPrice = state.LastDownShiftPrice
	}
	if !state.LastDownShiftAt.IsZero() {
		s.lastDownShiftAt = state.LastDownShiftAt
	}
	if state.ContractMode != "" {
		s.ContractMode = normalizeContractMode(ContractMode(state.ContractMode))
	}
}

func (s *FuturesGrid) SetAlerter(alerter alert.Alerter) {
	s.alerter = alerter
}

func (s *FuturesGrid) SetSellRatio(ratio decimal.Decimal) {
	if ratio.Cmp(decimal.NewFromInt(1)) > 0 {
		s.SellRatio = ratio
	}
}

func (s *FuturesGrid) SetRatioStep(step decimal.Decimal) {
	if step.Cmp(decimal.Zero) >= 0 {
		s.RatioStep = step
	}
}

func (s *FuturesGrid) SetRatioQtyMultiple(v decimal.Decimal) {
	if v.Cmp(decimal.Zero) > 0 {
		s.RatioQtyMultiple = v
	}
}

func (s *FuturesGrid) SetContractMode(mode ContractMode) {
	s.ContractMode = normalizeContractMode(mode)
}

func (s *FuturesGrid) Init(ctx context.Context, price decimal.Decimal) error {
	if s.stopped {
		return ErrStopped
	}
	if s.initialized {
		return nil
	}
	if s.shouldStop(price) {
		return s.stopNow(ctx)
	}
	if s.Qty.Cmp(decimal.Zero) <= 0 {
		return errors.New("qty must be > 0")
	}
	if s.Ratio.Cmp(decimal.NewFromInt(1)) <= 0 {
		return errors.New("ratio must be > 1")
	}
	if s.baseBuyRatio.Cmp(decimal.NewFromInt(1)) <= 0 {
		s.baseBuyRatio = s.Ratio
	}
	if s.SellRatio.Cmp(decimal.NewFromInt(1)) <= 0 {
		s.SellRatio = s.Ratio
	}
	if s.SellRatio.Cmp(decimal.NewFromInt(1)) <= 0 {
		return errors.New("sell_ratio must be > 1")
	}
	if s.anchor.Cmp(decimal.Zero) <= 0 {
		s.anchor = price
	}
	if err := s.initWindowBounds(); err != nil {
		return err
	}

	switch s.mode() {
	case ContractModeLong:
		for i := -1; i >= s.minLevel; i-- {
			if err := s.placeLimit(ctx, core.Buy, i); err != nil {
				s.alertImportant("bootstrap_failed", map[string]string{
					"stage": "place_initial_long_buy",
					"level": strconv.Itoa(i),
					"err":   err.Error(),
				})
				_ = s.persistSnapshot()
				return err
			}
		}
	case ContractModeShort:
		for i := 1; i <= s.maxLevel; i++ {
			if err := s.placeLimit(ctx, core.Sell, i); err != nil {
				s.alertImportant("bootstrap_failed", map[string]string{
					"stage": "place_initial_short_sell",
					"level": strconv.Itoa(i),
					"err":   err.Error(),
				})
				_ = s.persistSnapshot()
				return err
			}
		}
	default:
		for i := 1; i <= s.maxLevel; i++ {
			if err := s.placeLimit(ctx, core.Sell, i); err != nil {
				s.alertImportant("bootstrap_failed", map[string]string{
					"stage": "place_initial_sell",
					"level": strconv.Itoa(i),
					"err":   err.Error(),
				})
				_ = s.persistSnapshot()
				return err
			}
		}
		for i := -1; i >= s.minLevel; i-- {
			if err := s.placeLimit(ctx, core.Buy, i); err != nil {
				s.alertImportant("bootstrap_failed", map[string]string{
					"stage": "place_initial_buy",
					"level": strconv.Itoa(i),
					"err":   err.Error(),
				})
				_ = s.persistSnapshot()
				return err
			}
		}
	}

	s.initialized = true
	if err := s.persistSnapshot(); err != nil {
		s.alertImportant("bootstrap_failed", map[string]string{
			"stage": "persist_bootstrap_state",
			"err":   err.Error(),
		})
		return err
	}
	return nil
}

func (s *FuturesGrid) initWindowBounds() error {
	switch s.mode() {
	case ContractModeLong:
		if s.minLevel == 0 {
			s.minLevel = -s.Levels
		}
		if s.maxLevel == 0 {
			s.maxLevel = 0
		}
		if s.minLevel > -1 {
			return errors.New("levels must be >= 1 for long mode")
		}
	case ContractModeShort:
		if s.maxLevel == 0 {
			s.maxLevel = s.Levels
		}
		if s.minLevel == 0 {
			s.minLevel = 0
		}
		if s.maxLevel < 1 {
			return errors.New("levels must be >= 1 for short mode")
		}
	default:
		if s.maxLevel == 0 {
			s.maxLevel = s.Levels
		}
		if s.minLevel == 0 && s.maxLevel <= s.Levels {
			s.minLevel = -s.Levels
		}
		if s.maxLevel < 1 {
			return errors.New("levels must be >= 1 for dual mode")
		}
	}
	return nil
}

func (s *FuturesGrid) OnFill(ctx context.Context, trade core.Trade) error {
	if s.stopped {
		return ErrStopped
	}
	if trade.Status == "" {
		trade.Status = core.OrderFilled
	}

	ord, ok := s.openOrders[trade.OrderID]
	if ok {
		if trade.Qty.Cmp(decimal.Zero) > 0 && trade.Qty.Cmp(ord.Qty) < 0 && trade.Status == core.OrderPartiallyFilled {
			ord.Qty = ord.Qty.Sub(trade.Qty)
			s.openOrders[trade.OrderID] = ord
			if err := s.appendTrade(trade); err != nil {
				return err
			}
			if s.shouldStop(trade.Price) {
				return s.stopNow(ctx)
			}
			return s.persistSnapshot()
		}
		delete(s.openOrders, trade.OrderID)
	}

	if err := s.appendTrade(trade); err != nil {
		return err
	}
	if s.shouldStop(trade.Price) {
		return s.stopNow(ctx)
	}

	side := trade.Side
	idx := ord.GridIndex
	if isOrderClosedWithoutFullFill(trade.Status) {
		return s.persistSnapshot()
	}
	if !ok {
		if trade.Status != core.OrderFilled {
			return s.persistSnapshot()
		}
		var idxOk bool
		idx, idxOk = s.indexForPrice(trade.Price)
		if !idxOk {
			return s.persistSnapshot()
		}
	}

	if err := s.applyFillTransition(ctx, side, idx, trade); err != nil {
		_ = s.persistSnapshot()
		return err
	}
	return s.persistSnapshot()
}

func (s *FuturesGrid) appendTrade(trade core.Trade) error {
	if s.store == nil {
		return nil
	}
	if err := s.store.AppendTrade(trade); err != nil {
		s.alertImportant("state_persist_failed", map[string]string{
			"stage": "append_trade",
			"err":   err.Error(),
		})
		_ = s.persistSnapshot()
		return err
	}
	return nil
}

func (s *FuturesGrid) applyFillTransition(ctx context.Context, side core.Side, idx int, trade core.Trade) error {
	switch s.mode() {
	case ContractModeLong:
		return s.onFillLong(ctx, side, idx, trade)
	case ContractModeShort:
		return s.onFillShort(ctx, side, idx, trade)
	default:
		return s.onFillDual(ctx, side, idx, trade)
	}
}

func (s *FuturesGrid) onFillDual(ctx context.Context, side core.Side, idx int, trade core.Trade) error {
	switch side {
	case core.Sell:
		if err := s.placeLimit(ctx, core.Buy, idx-1); err != nil {
			return err
		}
		if idx == s.maxLevel {
			if err := s.shiftUp(ctx, idx, trade.Price, trade.Time); err != nil {
				return err
			}
		}
	case core.Buy:
		if err := s.placeLimit(ctx, core.Sell, idx+1); err != nil {
			return err
		}
		if idx == s.minLevel {
			s.onDownShiftTriggered(trade.Price, trade.Time)
			if err := s.extendDown(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *FuturesGrid) onFillLong(ctx context.Context, side core.Side, idx int, trade core.Trade) error {
	switch side {
	case core.Buy:
		if err := s.placeLimit(ctx, core.Sell, idx+1); err != nil {
			return err
		}
		if idx == s.minLevel {
			s.onDownShiftTriggered(trade.Price, trade.Time)
			if err := s.extendDown(ctx); err != nil {
				return err
			}
		}
	case core.Sell:
		if err := s.placeLimit(ctx, core.Buy, idx-1); err != nil {
			return err
		}
	}
	return nil
}

func (s *FuturesGrid) onFillShort(ctx context.Context, side core.Side, idx int, _ core.Trade) error {
	switch side {
	case core.Sell:
		if err := s.placeLimit(ctx, core.Buy, idx-1); err != nil {
			return err
		}
		if idx == s.maxLevel {
			if err := s.extendUp(ctx); err != nil {
				return err
			}
		}
	case core.Buy:
		if err := s.placeLimit(ctx, core.Sell, idx+1); err != nil {
			return err
		}
	}
	return nil
}

func (s *FuturesGrid) OnTick(ctx context.Context, price decimal.Decimal, _ time.Time) error {
	if s.stopped {
		return ErrStopped
	}
	if s.shouldStop(price) {
		return s.stopNow(ctx)
	}
	if !s.initialized {
		return nil
	}
	if s.anchor.Cmp(decimal.Zero) <= 0 {
		s.anchor = price
	}
	if s.SellRatio.Cmp(decimal.NewFromInt(1)) <= 0 {
		s.SellRatio = s.Ratio
	}
	return s.initWindowBounds()
}

func (s *FuturesGrid) Reconcile(ctx context.Context, price decimal.Decimal, openOrders []core.Order) error {
	if s.stopped {
		return s.reconcileStopped(ctx, openOrders)
	}
	if s.shouldStop(price) {
		s.replaceOpenOrdersFromExchange(openOrders)
		return s.stopNow(ctx)
	}
	if s.anchor.Cmp(decimal.Zero) <= 0 {
		s.anchor = price
	}
	if s.SellRatio.Cmp(decimal.NewFromInt(1)) <= 0 {
		s.SellRatio = s.Ratio
	}
	if err := s.initWindowBounds(); err != nil {
		return err
	}
	s.initialized = false

	s.openOrders = make(map[string]core.Order)
	type reconcileBucketKey struct {
		Level        int
		Side         core.Side
		PositionSide core.PositionSide
	}
	levelBuckets := make(map[reconcileBucketKey][]core.Order)
	for _, ord := range openOrders {
		idx, ok := s.indexForPrice(ord.Price)
		if !ok {
			continue
		}
		ord.GridIndex = idx
		if ord.PositionSide == "" {
			ord.PositionSide, _ = s.orderFlags(ord.Side, idx)
		}
		key := reconcileBucketKey{
			Level:        idx,
			Side:         ord.Side,
			PositionSide: ord.PositionSide,
		}
		levelBuckets[key] = append(levelBuckets[key], ord)
	}

	keys := make([]reconcileBucketKey, 0, len(levelBuckets))
	for k := range levelBuckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Level != keys[j].Level {
			return keys[i].Level < keys[j].Level
		}
		if keys[i].Side != keys[j].Side {
			return keys[i].Side < keys[j].Side
		}
		return keys[i].PositionSide < keys[j].PositionSide
	})

	for _, key := range keys {
		idx := key.Level
		ordersAtLevel := levelBuckets[key]
		keepIdx := primaryOrderIndex(ordersAtLevel)
		keep := ordersAtLevel[keepIdx]
		if keep.ID != "" {
			s.openOrders[keep.ID] = keep
		}

		for i, ord := range ordersAtLevel {
			if i == keepIdx || ord.ID == "" {
				continue
			}
			if err := s.executor.CancelOrder(ctx, s.Symbol, ord.ID); err != nil {
				s.alertImportant("reconcile_duplicate_order_cancel_failed", map[string]string{
					"order_id": idOrPlaceholder(ord.ID),
					"side":     string(ord.Side),
					"position": string(ord.PositionSide),
					"price":    ord.Price.String(),
					"qty":      ord.Qty.String(),
					"level":    strconv.Itoa(idx),
					"err":      err.Error(),
				})
				_ = s.persistSnapshot()
				return err
			}
			s.alertImportant("reconcile_duplicate_order_canceled", map[string]string{
				"order_id": idOrPlaceholder(ord.ID),
				"side":     string(ord.Side),
				"position": string(ord.PositionSide),
				"price":    ord.Price.String(),
				"qty":      ord.Qty.String(),
				"level":    strconv.Itoa(idx),
				"kept_id":  idOrPlaceholder(keep.ID),
			})
		}
	}

	switch s.mode() {
	case ContractModeLong:
		for i := -1; i >= s.minLevel; i-- {
			if s.hasOrderLevelWithSide(core.Buy, i) {
				continue
			}
			if err := s.placeLimit(ctx, core.Buy, i); err != nil {
				s.alertImportant("reconcile_gap_order_failed", map[string]string{
					"side":  string(core.Buy),
					"level": strconv.Itoa(i),
					"err":   err.Error(),
				})
				_ = s.persistSnapshot()
				return err
			}
		}
	case ContractModeShort:
		for i := 1; i <= s.maxLevel; i++ {
			if s.hasOrderLevelWithSide(core.Sell, i) {
				continue
			}
			if err := s.placeLimit(ctx, core.Sell, i); err != nil {
				s.alertImportant("reconcile_gap_order_failed", map[string]string{
					"side":  string(core.Sell),
					"level": strconv.Itoa(i),
					"err":   err.Error(),
				})
				_ = s.persistSnapshot()
				return err
			}
		}
	default:
		for i := 1; i <= s.maxLevel; i++ {
			if s.hasOrderLevelWithSide(core.Sell, i) {
				continue
			}
			if err := s.placeLimit(ctx, core.Sell, i); err != nil {
				s.alertImportant("reconcile_gap_order_failed", map[string]string{
					"side":  string(core.Sell),
					"level": strconv.Itoa(i),
					"err":   err.Error(),
				})
				_ = s.persistSnapshot()
				return err
			}
		}
		for i := -1; i >= s.minLevel; i-- {
			if s.hasOrderLevelWithSide(core.Buy, i) {
				continue
			}
			if err := s.placeLimit(ctx, core.Buy, i); err != nil {
				s.alertImportant("reconcile_gap_order_failed", map[string]string{
					"side":  string(core.Buy),
					"level": strconv.Itoa(i),
					"err":   err.Error(),
				})
				_ = s.persistSnapshot()
				return err
			}
		}
	}

	s.initialized = true
	if err := s.persistSnapshot(); err != nil {
		s.alertImportant("reconcile_persist_failed", map[string]string{
			"err": err.Error(),
		})
		return err
	}
	return nil
}

func (s *FuturesGrid) reconcileStopped(ctx context.Context, openOrders []core.Order) error {
	s.replaceOpenOrdersFromExchange(openOrders)
	return s.stopNow(ctx)
}

func (s *FuturesGrid) Reset() {
	s.openOrders = make(map[string]core.Order)
	s.initialized = false
	s.stopped = false
	if s.baseBuyRatio.Cmp(decimal.NewFromInt(1)) > 0 {
		s.Ratio = s.baseBuyRatio
	}
	s.lastDownShiftPrice = decimal.Zero
	s.lastDownShiftAt = time.Time{}
	_ = s.persistSnapshot()
}

func (s *FuturesGrid) orderQty() decimal.Decimal {
	qty := s.Qty
	if s.minQtyMultiple > 0 && s.rules.MinQty.Cmp(decimal.Zero) > 0 {
		minQty := s.rules.MinQty.Mul(decimal.NewFromInt(s.minQtyMultiple))
		if qty.Cmp(minQty) < 0 {
			qty = minQty
		}
	}
	return qty
}

func (s *FuturesGrid) effectiveRatios() (decimal.Decimal, decimal.Decimal) {
	one := decimal.NewFromInt(1)
	buy := s.Ratio
	if buy.Cmp(one) <= 0 {
		buy = decimal.RequireFromString("1.000001")
	}
	sell := s.SellRatio
	if sell.Cmp(one) <= 0 {
		sell = buy
	}
	if buy.Cmp(one) <= 0 {
		buy = s.Ratio
	}
	if sell.Cmp(one) <= 0 {
		sell = s.SellRatio
	}
	return buy, sell
}

func (s *FuturesGrid) priceForLevel(idx int) decimal.Decimal {
	if s.anchor.Cmp(decimal.Zero) <= 0 {
		return decimal.Zero
	}
	buyRatio, sellRatio := s.effectiveRatios()
	price := s.anchor
	switch {
	case idx > 0:
		price = s.anchor.Mul(powDecimal(sellRatio, idx))
	case idx < 0:
		price = s.anchor.Div(powDecimal(buyRatio, -idx))
	}
	if s.rules.PriceTick.Cmp(decimal.Zero) > 0 {
		price = core.RoundDown(price, s.rules.PriceTick)
	}
	return price
}

func (s *FuturesGrid) indexForPrice(price decimal.Decimal) (int, bool) {
	if s.anchor.Cmp(decimal.Zero) <= 0 {
		return 0, false
	}
	target := price
	if s.rules.PriceTick.Cmp(decimal.Zero) > 0 {
		target = core.RoundDown(price, s.rules.PriceTick)
	}

	minIdx := s.minLevel
	maxIdx := s.maxLevel
	for idx := minIdx; idx <= maxIdx; idx++ {
		if s.priceForLevel(idx).Cmp(target) == 0 {
			return idx, true
		}
	}
	return 0, false
}

func (s *FuturesGrid) placeLimit(ctx context.Context, side core.Side, idx int) error {
	return s.placeLimitWithQtyMultiple(ctx, side, idx, decimal.NewFromInt(1))
}

func (s *FuturesGrid) placeLimitWithQtyMultiple(ctx context.Context, side core.Side, idx int, qtyMultiple decimal.Decimal) error {
	if !s.allowLevel(side, idx) {
		return nil
	}
	positionSide, reduceOnly := s.orderFlags(side, idx)
	if s.hasOrderLevelWithSide(side, idx) {
		return nil
	}
	price := s.priceForLevel(idx)
	if price.Cmp(decimal.Zero) <= 0 {
		return nil
	}
	qty := s.orderQty()
	if qtyMultiple.Cmp(decimal.Zero) > 0 {
		qty = qty.Mul(qtyMultiple)
	}
	if qty.Cmp(decimal.Zero) <= 0 {
		return nil
	}
	order := core.Order{
		Symbol:       s.Symbol,
		Side:         side,
		Type:         core.Limit,
		PositionSide: positionSide,
		ReduceOnly:   reduceOnly,
		Price:        price,
		Qty:          qty,
		GridIndex:    idx,
		CreatedAt:    time.Now().UTC(),
	}
	norm, err := core.NormalizeOrder(order, s.rules)
	if err != nil {
		return err
	}
	order = norm
	placed, err := s.executor.PlaceOrder(ctx, order)
	if err != nil {
		if isInsufficientBalanceError(err) {
			s.alertImportant("place_order_skipped_insufficient_balance", map[string]string{
				"side":  string(side),
				"level": strconv.Itoa(idx),
				"price": order.Price.String(),
				"qty":   order.Qty.String(),
				"err":   err.Error(),
			})
			return nil
		}
		return err
	}
	if placed.CreatedAt.IsZero() {
		placed.CreatedAt = order.CreatedAt
	}
	if placed.PositionSide == "" {
		placed.PositionSide = order.PositionSide
	}
	if !placed.ReduceOnly {
		placed.ReduceOnly = order.ReduceOnly
	}
	placed.GridIndex = idx
	s.openOrders[placed.ID] = placed
	return nil
}

func (s *FuturesGrid) allowLevel(side core.Side, idx int) bool {
	switch s.mode() {
	case ContractModeLong:
		if side == core.Buy {
			return idx >= s.minLevel && idx <= -1
		}
		return idx >= s.minLevel+1 && idx <= 0
	case ContractModeShort:
		if side == core.Sell {
			return idx >= 1 && idx <= s.maxLevel
		}
		return idx >= 0 && idx <= s.maxLevel-1
	default:
		return idx <= s.maxLevel
	}
}

func (s *FuturesGrid) orderFlags(side core.Side, idx int) (core.PositionSide, bool) {
	switch s.mode() {
	case ContractModeLong:
		return core.PositionSideBoth, side == core.Sell
	case ContractModeShort:
		return core.PositionSideBoth, side == core.Buy
	default:
		switch {
		case side == core.Buy && idx <= -1:
			return core.PositionSideLong, false
		case side == core.Sell && idx >= 1:
			return core.PositionSideShort, false
		case side == core.Buy && idx >= 0:
			return core.PositionSideShort, true
		case side == core.Sell && idx <= 0:
			return core.PositionSideLong, true
		default:
			return core.PositionSideBoth, false
		}
	}
}

func (s *FuturesGrid) extendDown(ctx context.Context) error {
	shift := s.shiftLevels()
	if shift <= 0 {
		return nil
	}
	oldMin := s.minLevel
	s.minLevel = s.minLevel - shift
	qtyMultiple := s.downShiftQtyMultiple()
	for i := oldMin - 1; i >= s.minLevel; i-- {
		if err := s.placeLimitWithQtyMultiple(ctx, core.Buy, i, qtyMultiple); err != nil {
			return err
		}
	}
	return nil
}

func (s *FuturesGrid) extendUp(ctx context.Context) error {
	shift := s.shiftLevels()
	if shift <= 0 {
		return nil
	}
	oldMax := s.maxLevel
	s.maxLevel = s.maxLevel + shift
	for i := oldMax + 1; i <= s.maxLevel; i++ {
		if err := s.placeLimit(ctx, core.Sell, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *FuturesGrid) shiftUp(ctx context.Context, filledLevel int, triggerPrice decimal.Decimal, at time.Time) error {
	shift := s.shiftLevels()
	if shift < 1 {
		return nil
	}
	oldMin := s.minLevel
	oldMax := s.maxLevel
	if filledLevel != oldMax {
		return nil
	}
	s.restoreBuyRatioOnShiftUp(triggerPrice, at)
	newMin := oldMin + shift
	newMax := oldMax + shift
	if err := s.cancelBuyRange(ctx, oldMin, oldMin+shift-1); err != nil {
		return err
	}
	s.minLevel = newMin
	s.maxLevel = newMax
	for i := oldMax + 1; i <= newMax; i++ {
		if err := s.placeLimit(ctx, core.Sell, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *FuturesGrid) shouldStop(price decimal.Decimal) bool {
	if s.StopPrice.Cmp(decimal.Zero) <= 0 {
		return false
	}
	if price.Cmp(decimal.Zero) <= 0 {
		return false
	}
	return price.Cmp(s.StopPrice) > 0
}

func (s *FuturesGrid) stopNow(ctx context.Context) error {
	justStopped := !s.stopped
	s.cancelAllOpenOrders(ctx)
	s.stopped = true
	s.initialized = false
	if justStopped {
		s.alertImportant("strategy_stop_price_triggered", map[string]string{
			"symbol":     s.Symbol,
			"stop_price": s.StopPrice.String(),
		})
	}
	if err := s.persistSnapshot(); err != nil {
		return err
	}
	if len(s.openOrders) > 0 {
		return nil
	}
	return ErrStopped
}

func (s *FuturesGrid) replaceOpenOrdersFromExchange(openOrders []core.Order) {
	next := make(map[string]core.Order, len(openOrders))
	for _, ord := range openOrders {
		if ord.ID == "" {
			continue
		}
		if idx, ok := s.indexForPrice(ord.Price); ok {
			ord.GridIndex = idx
		}
		next[ord.ID] = ord
	}
	s.openOrders = next
}

func (s *FuturesGrid) cancelAllOpenOrders(ctx context.Context) {
	for id, ord := range s.openOrders {
		if id == "" {
			delete(s.openOrders, id)
			continue
		}
		if err := s.executor.CancelOrder(ctx, s.Symbol, id); err != nil {
			s.alertImportant("cancel_order_failed", map[string]string{
				"order_id": id,
				"side":     string(ord.Side),
				"price":    ord.Price.String(),
				"qty":      ord.Qty.String(),
				"err":      err.Error(),
			})
			continue
		}
		delete(s.openOrders, id)
	}
}

func (s *FuturesGrid) cancelBuyRange(ctx context.Context, from, to int) error {
	var firstErr error
	for id, ord := range s.openOrders {
		if ord.Side != core.Buy {
			continue
		}
		if ord.GridIndex < from || ord.GridIndex > to {
			continue
		}
		if err := s.executor.CancelOrder(ctx, s.Symbol, id); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			s.alertImportant("cancel_order_failed", map[string]string{
				"order_id": id,
				"side":     string(ord.Side),
				"price":    ord.Price.String(),
				"qty":      ord.Qty.String(),
				"err":      err.Error(),
			})
			continue
		}
		delete(s.openOrders, id)
	}
	return firstErr
}

func (s *FuturesGrid) cancelConflictingOrderAtLevel(ctx context.Context, idx int, expectedSide core.Side) error {
	for id, ord := range s.openOrders {
		if ord.GridIndex != idx {
			continue
		}
		if ord.Side == expectedSide {
			continue
		}
		if id == "" {
			delete(s.openOrders, id)
			continue
		}
		if err := s.executor.CancelOrder(ctx, s.Symbol, id); err != nil {
			s.alertImportant("cancel_order_failed", map[string]string{
				"order_id": id,
				"side":     string(ord.Side),
				"price":    ord.Price.String(),
				"qty":      ord.Qty.String(),
				"err":      err.Error(),
			})
			return err
		}
		delete(s.openOrders, id)
		s.alertImportant("reconcile_conflict_order_canceled", map[string]string{
			"order_id":      id,
			"side":          string(ord.Side),
			"expected_side": string(expectedSide),
			"level":         strconv.Itoa(idx),
			"price":         ord.Price.String(),
			"qty":           ord.Qty.String(),
		})
	}
	return nil
}

func (s *FuturesGrid) shiftLevels() int {
	if s.Shift > 0 {
		return s.Shift
	}
	if s.Levels < 2 {
		return 1
	}
	return s.Levels / 2
}

func (s *FuturesGrid) downShiftRatioStep() decimal.Decimal {
	return s.RatioStep
}

func (s *FuturesGrid) downShiftQtyMultiple() decimal.Decimal {
	if s.RatioQtyMultiple.Cmp(decimal.Zero) > 0 {
		return s.RatioQtyMultiple
	}
	return decimal.RequireFromString(defaultFuturesRatioQtyMultiple)
}

func (s *FuturesGrid) onDownShiftTriggered(triggerPrice decimal.Decimal, at time.Time) {
	if triggerPrice.Cmp(decimal.Zero) <= 0 {
		return
	}
	step := s.downShiftRatioStep()
	if step.Cmp(decimal.Zero) <= 0 {
		return
	}
	if s.baseBuyRatio.Cmp(decimal.NewFromInt(1)) <= 0 && s.Ratio.Cmp(decimal.NewFromInt(1)) > 0 {
		s.baseBuyRatio = s.Ratio
	}
	oldRatio := s.Ratio
	s.Ratio = s.Ratio.Add(step)
	s.alertImportant("buy_ratio_defense_raised", map[string]string{
		"old_ratio":      oldRatio.String(),
		"new_ratio":      s.Ratio.String(),
		"trigger_price":  triggerPrice.String(),
		"previous_price": s.lastDownShiftPrice.String(),
	})
	s.lastDownShiftPrice = triggerPrice
	if at.IsZero() {
		at = time.Now().UTC()
	}
	s.lastDownShiftAt = at
}

func (s *FuturesGrid) restoreBuyRatioOnShiftUp(price decimal.Decimal, at time.Time) {
	if s.baseBuyRatio.Cmp(decimal.NewFromInt(1)) <= 0 {
		return
	}
	if s.Ratio.Cmp(s.baseBuyRatio) <= 0 {
		return
	}
	oldRatio := s.Ratio
	s.Ratio = s.baseBuyRatio
	now := at
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if price.Cmp(decimal.Zero) > 0 {
		s.lastDownShiftPrice = price
	}
	s.lastDownShiftAt = now
	s.alertImportant("buy_ratio_defense_restored", map[string]string{
		"old_ratio":       oldRatio.String(),
		"new_ratio":       s.Ratio.String(),
		"reference_price": s.lastDownShiftPrice.String(),
	})
}

func (s *FuturesGrid) hasOrderLevel(idx int) bool {
	for _, ord := range s.openOrders {
		if ord.GridIndex == idx {
			return true
		}
	}
	return false
}

func (s *FuturesGrid) hasOrderLevelWithSide(side core.Side, idx int) bool {
	for _, ord := range s.openOrders {
		if ord.GridIndex == idx && ord.Side == side {
			return true
		}
	}
	return false
}

func primaryOrderIndex(orders []core.Order) int {
	if len(orders) == 0 {
		return 0
	}
	best := 0
	for i := 1; i < len(orders); i++ {
		if preferOrder(orders[i], orders[best]) {
			best = i
		}
	}
	return best
}

func preferOrder(a, b core.Order) bool {
	aTimeSet := !a.CreatedAt.IsZero()
	bTimeSet := !b.CreatedAt.IsZero()
	if aTimeSet || bTimeSet {
		if aTimeSet && !bTimeSet {
			return true
		}
		if !aTimeSet && bTimeSet {
			return false
		}
		if a.CreatedAt.Before(b.CreatedAt) {
			return true
		}
		if a.CreatedAt.After(b.CreatedAt) {
			return false
		}
	}
	return a.ID < b.ID
}

func idOrPlaceholder(id string) string {
	if id == "" {
		return "unknown"
	}
	return id
}

func isOrderClosedWithoutFullFill(status core.OrderStatus) bool {
	switch status {
	case core.OrderCanceled, core.OrderExpired, core.OrderRejected:
		return true
	default:
		return false
	}
}

func isInsufficientBalanceError(err error) bool {
	return errors.Is(err, core.ErrInsufficientBalance)
}

func (s *FuturesGrid) persistSnapshot() error {
	if s.store == nil {
		return nil
	}
	state := s.snapshotState()
	state.SnapshotID = strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	if err := s.store.SaveGridState(state); err != nil {
		s.alertImportant("state_persist_failed", map[string]string{
			"stage": "save_grid_state",
			"err":   err.Error(),
		})
		return err
	}
	if err := s.store.SaveOpenOrders(s.snapshotOrders()); err != nil {
		s.alertImportant("state_persist_failed", map[string]string{
			"stage": "save_open_orders",
			"err":   err.Error(),
		})
		return err
	}
	return nil
}

func (s *FuturesGrid) alertImportant(event string, fields map[string]string) {
	if s.alerter == nil {
		return
	}
	s.alerter.Important(event, fields)
}

func (s *FuturesGrid) snapshotState() store.GridState {
	state := store.GridState{
		Strategy:           "futures_grid",
		Symbol:             s.Symbol,
		ContractMode:       string(s.mode()),
		Anchor:             s.anchor,
		StopPrice:          s.StopPrice,
		Ratio:              s.Ratio,
		BaseRatio:          s.baseBuyRatio,
		SellRatio:          s.SellRatio,
		Levels:             s.Levels,
		MinLevel:           s.minLevel,
		MaxLevel:           s.maxLevel,
		Qty:                s.Qty,
		MinQtyMultiple:     s.minQtyMultiple,
		Rules:              s.rules,
		Initialized:        s.initialized,
		Stopped:            s.stopped,
		LastDownShiftPrice: s.lastDownShiftPrice,
		LastDownShiftAt:    s.lastDownShiftAt,
	}
	if s.minLevel != 0 {
		state.Low = s.priceForLevel(s.minLevel)
	}
	return state
}

func (s *FuturesGrid) snapshotOrders() []core.Order {
	orders := make([]core.Order, 0, len(s.openOrders))
	for _, ord := range s.openOrders {
		orders = append(orders, ord)
	}
	sort.Slice(orders, func(i, j int) bool {
		if orders[i].GridIndex != orders[j].GridIndex {
			return orders[i].GridIndex < orders[j].GridIndex
		}
		if orders[i].Side != orders[j].Side {
			return orders[i].Side < orders[j].Side
		}
		if orders[i].Price.Cmp(orders[j].Price) != 0 {
			return orders[i].Price.Cmp(orders[j].Price) < 0
		}
		return orders[i].ID < orders[j].ID
	})
	return orders
}

func powDecimal(base decimal.Decimal, exp int) decimal.Decimal {
	if exp == 0 {
		return decimal.NewFromInt(1)
	}
	if exp < 0 {
		return decimal.NewFromInt(1).Div(powDecimal(base, -exp))
	}
	result := decimal.NewFromInt(1)
	for i := 0; i < exp; i++ {
		result = result.Mul(base)
	}
	return result
}
