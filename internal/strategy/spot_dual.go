package strategy

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"grid-trading/internal/alert"
	"grid-trading/internal/core"
	"grid-trading/internal/store"
)

type OrderExecutor interface {
	PlaceOrder(ctx context.Context, order core.Order) (core.Order, error)
	CancelOrder(ctx context.Context, symbol, orderID string) error
	Balances(ctx context.Context) (core.Balance, error)
}

type SpotDual struct {
	Symbol    string
	StopPrice decimal.Decimal
	Ratio     decimal.Decimal
	SellRatio decimal.Decimal
	Levels    int
	Shift     int
	Qty       decimal.Decimal

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
	regimeCfg   RegimeControlConfig
	regimeState RegimeState
	regime      *regimeDetector
	qtyScale    decimal.Decimal
}

func NewSpotDual(symbol string, stopPrice, ratio decimal.Decimal, levels, shift int, qty decimal.Decimal, minQtyMultiple int64, rules core.Rules, store store.Persister, executor OrderExecutor) *SpotDual {
	return &SpotDual{
		Symbol:         symbol,
		StopPrice:      stopPrice,
		Ratio:          ratio,
		SellRatio:      ratio,
		Levels:         levels,
		Shift:          shift,
		Qty:            qty,
		minQtyMultiple: minQtyMultiple,
		rules:          rules,
		executor:       executor,
		openOrders:     make(map[string]core.Order),
		store:          store,
		ignoreFills:    make(map[string]struct{}),
		regimeState:    RegimeRange,
		qtyScale:       decimal.NewFromInt(1),
	}
}

func (s *SpotDual) LoadState(state store.GridState) {
	if state.Symbol != "" && state.Symbol != s.Symbol {
		return
	}
	if state.StopPrice.Cmp(decimal.Zero) > 0 {
		s.StopPrice = state.StopPrice
	}
	if state.Ratio.Cmp(decimal.NewFromInt(1)) > 0 {
		s.Ratio = state.Ratio
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
}

func (s *SpotDual) SetAlerter(alerter alert.Alerter) {
	s.alerter = alerter
}

func (s *SpotDual) SetSellRatio(ratio decimal.Decimal) {
	if ratio.Cmp(decimal.NewFromInt(1)) > 0 {
		s.SellRatio = ratio
	}
}

func (s *SpotDual) SetQtyScale(scale decimal.Decimal) {
	if scale.Cmp(decimal.Zero) > 0 && scale.Cmp(decimal.NewFromInt(1)) <= 0 {
		s.qtyScale = scale
	}
}

func (s *SpotDual) SetRegimeControl(cfg RegimeControlConfig) {
	cfg = normalizeRegimeConfig(cfg)
	s.regimeCfg = cfg
	if !cfg.Enabled {
		s.regime = nil
		s.regimeState = RegimeRange
		return
	}
	s.regime = newRegimeDetector(cfg)
	s.regimeState = RegimeRange
}

func (s *SpotDual) Init(ctx context.Context, price decimal.Decimal) error {
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
	if s.SellRatio.Cmp(decimal.NewFromInt(1)) <= 0 {
		s.SellRatio = s.Ratio
	}
	if s.SellRatio.Cmp(decimal.NewFromInt(1)) <= 0 {
		return errors.New("sell_ratio must be > 1")
	}
	s.updateRegime(price)
	if s.anchor.Cmp(decimal.Zero) <= 0 {
		s.anchor = price
	}
	if s.maxLevel == 0 {
		s.maxLevel = s.sellLevels()
	}
	if s.minLevel == 0 && s.maxLevel <= s.Levels {
		s.minLevel = -s.Levels
	}
	if s.maxLevel < 1 {
		return errors.New("shift_levels must be >= 1")
	}

	orderQty := s.orderQty()
	totalBase := orderQty.Mul(decimal.NewFromInt(int64(s.maxLevel)))
	if totalBase.Cmp(decimal.Zero) > 0 {
		need, err := s.baseBuyNeed(ctx, totalBase)
		if err != nil {
			s.alertImportant("bootstrap_failed", map[string]string{
				"stage": "query_balance",
				"err":   err.Error(),
			})
			_ = s.persistSnapshot()
			return err
		}
		if need.Cmp(decimal.Zero) > 0 {
			if err := s.placeMarketBuy(ctx, need); err != nil {
				s.alertImportant("bootstrap_failed", map[string]string{
					"stage": "market_buy_base",
					"qty":   need.String(),
					"err":   err.Error(),
				})
				_ = s.persistSnapshot()
				return err
			}
		}
	}

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

func (s *SpotDual) baseBuyNeed(ctx context.Context, target decimal.Decimal) (decimal.Decimal, error) {
	if target.Cmp(decimal.Zero) <= 0 {
		return decimal.Zero, nil
	}
	bal, err := s.executor.Balances(ctx)
	if err != nil {
		return decimal.Zero, err
	}
	need := target.Sub(bal.Base)
	if need.Cmp(decimal.Zero) <= 0 {
		return decimal.Zero, nil
	}
	return need, nil
}

func (s *SpotDual) OnFill(ctx context.Context, trade core.Trade) error {
	if s.stopped {
		return ErrStopped
	}
	if trade.Status == "" {
		trade.Status = core.OrderFilled
	}
	if _, ok := s.ignoreFills[trade.OrderID]; ok {
		if s.store != nil {
			if err := s.store.AppendTrade(trade); err != nil {
				s.alertImportant("state_persist_failed", map[string]string{
					"stage": "append_trade",
					"err":   err.Error(),
				})
				_ = s.persistSnapshot()
				return err
			}
		}
		if trade.Status == core.OrderFilled || trade.Status == core.OrderCanceled || trade.Status == core.OrderExpired || trade.Status == core.OrderRejected {
			delete(s.ignoreFills, trade.OrderID)
		}
		if s.shouldStop(trade.Price) {
			return s.stopNow(ctx)
		}
		return s.persistSnapshot()
	}

	ord, ok := s.openOrders[trade.OrderID]
	if ok {
		if trade.Qty.Cmp(decimal.Zero) > 0 && trade.Qty.Cmp(ord.Qty) < 0 && trade.Status == core.OrderPartiallyFilled {
			ord.Qty = ord.Qty.Sub(trade.Qty)
			s.openOrders[trade.OrderID] = ord
			if s.store != nil {
				if err := s.store.AppendTrade(trade); err != nil {
					s.alertImportant("state_persist_failed", map[string]string{
						"stage": "append_trade",
						"err":   err.Error(),
					})
					_ = s.persistSnapshot()
					return err
				}
			}
			if s.shouldStop(trade.Price) {
				return s.stopNow(ctx)
			}
			return s.persistSnapshot()
		}
		delete(s.openOrders, trade.OrderID)
	}

	if s.store != nil {
		if err := s.store.AppendTrade(trade); err != nil {
			s.alertImportant("state_persist_failed", map[string]string{
				"stage": "append_trade",
				"err":   err.Error(),
			})
			_ = s.persistSnapshot()
			return err
		}
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

	switch side {
	case core.Sell:
		if err := s.placeLimit(ctx, core.Buy, idx-1); err != nil {
			_ = s.persistSnapshot()
			return err
		}
		if idx == s.maxLevel {
			if err := s.shiftUp(ctx, idx); err != nil {
				_ = s.persistSnapshot()
				return err
			}
		}
	case core.Buy:
		if err := s.placeLimit(ctx, core.Sell, idx+1); err != nil {
			_ = s.persistSnapshot()
			return err
		}
		if idx == s.minLevel {
			if err := s.extendDown(ctx); err != nil {
				_ = s.persistSnapshot()
				return err
			}
		}
	}
	return s.persistSnapshot()
}

func (s *SpotDual) OnTick(ctx context.Context, price decimal.Decimal, at time.Time) error {
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
	if s.maxLevel == 0 {
		s.maxLevel = s.sellLevels()
	}
	if s.minLevel == 0 && s.maxLevel <= s.Levels {
		s.minLevel = -s.Levels
	}
	regimeChanged := s.updateRegimeAt(price, at)
	if !regimeChanged {
		return nil
	}
	if err := s.rebuildOrders(ctx, price); err != nil {
		s.alertImportant("regime_rebuild_failed", map[string]string{
			"regime": string(s.regimeState),
			"err":    err.Error(),
		})
		_ = s.persistSnapshot()
		return err
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

func (s *SpotDual) Reconcile(ctx context.Context, price decimal.Decimal, openOrders []core.Order) error {
	if s.stopped {
		return ErrStopped
	}
	if s.shouldStop(price) {
		s.openOrders = make(map[string]core.Order, len(openOrders))
		for _, ord := range openOrders {
			s.openOrders[ord.ID] = ord
		}
		return s.stopNow(ctx)
	}
	if s.anchor.Cmp(decimal.Zero) <= 0 {
		s.anchor = price
	}
	if s.SellRatio.Cmp(decimal.NewFromInt(1)) <= 0 {
		s.SellRatio = s.Ratio
	}
	if s.maxLevel == 0 {
		s.maxLevel = s.sellLevels()
	}
	if s.minLevel == 0 && s.maxLevel <= s.Levels {
		s.minLevel = -s.Levels
	}
	regimeChanged := s.updateRegime(price)
	if regimeChanged {
		s.openOrders = make(map[string]core.Order, len(openOrders))
		for _, ord := range openOrders {
			if ord.ID == "" {
				continue
			}
			s.openOrders[ord.ID] = ord
		}
		if err := s.rebuildOrders(ctx, price); err != nil {
			s.alertImportant("regime_rebuild_failed", map[string]string{
				"regime": string(s.regimeState),
				"err":    err.Error(),
			})
			_ = s.persistSnapshot()
			return err
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

	s.openOrders = make(map[string]core.Order)
	levelOrders := make(map[int]core.Side)
	lowestBuy := 0
	for _, ord := range openOrders {
		idx, ok := s.indexForPrice(ord.Price)
		if !ok {
			continue
		}
		ord.GridIndex = idx
		s.openOrders[ord.ID] = ord
		if _, exists := levelOrders[idx]; !exists {
			levelOrders[idx] = ord.Side
		}
		if ord.Side == core.Buy {
			if lowestBuy == 0 || idx < lowestBuy {
				lowestBuy = idx
			}
		}
	}
	if lowestBuy != 0 && lowestBuy < s.minLevel {
		s.minLevel = lowestBuy
	}

	for i := 1; i <= s.maxLevel; i++ {
		if _, exists := levelOrders[i]; exists {
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
		levelOrders[i] = core.Sell
	}
	for i := -1; i >= s.minLevel; i-- {
		if _, exists := levelOrders[i]; exists {
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
		levelOrders[i] = core.Buy
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

func (s *SpotDual) Reset() {
	s.openOrders = make(map[string]core.Order)
	s.initialized = false
	s.stopped = false
	_ = s.persistSnapshot()
}

func (s *SpotDual) orderQty() decimal.Decimal {
	qty := s.Qty
	if s.qtyScale.Cmp(decimal.Zero) > 0 && s.qtyScale.Cmp(decimal.NewFromInt(1)) < 0 {
		qty = qty.Mul(s.qtyScale)
	}
	if s.minQtyMultiple > 0 && s.rules.MinQty.Cmp(decimal.Zero) > 0 {
		minQty := s.rules.MinQty.Mul(decimal.NewFromInt(s.minQtyMultiple))
		if qty.Cmp(minQty) < 0 {
			qty = minQty
		}
	}
	return qty
}

func (s *SpotDual) orderQtyForSide(side core.Side) decimal.Decimal {
	qty := s.orderQty()
	if side != core.Sell || s.regimeState != RegimeTrendUp || !s.regimeCfg.Enabled {
		return qty
	}
	minQty := s.rules.MinQty
	if minQty.Cmp(decimal.Zero) > 0 && qty.Cmp(minQty) <= 0 {
		return qty
	}
	factor := s.regimeCfg.TrendUpSellQtyFactor
	if factor <= 0 || factor >= 1 {
		return qty
	}
	scaled := qty.Mul(decimal.NewFromFloat(factor))
	if minQty.Cmp(decimal.Zero) > 0 && scaled.Cmp(minQty) < 0 {
		scaled = minQty
	}
	return scaled
}

func (s *SpotDual) effectiveRatios() (decimal.Decimal, decimal.Decimal) {
	one := decimal.NewFromInt(1)
	buy := s.Ratio
	if buy.Cmp(one) <= 0 {
		buy = decimal.RequireFromString("1.000001")
	}
	sell := s.SellRatio
	if sell.Cmp(one) <= 0 {
		sell = buy
	}
	if s.regimeCfg.Enabled {
		switch s.regimeState {
		case RegimeTrendUp:
			buy = scaleSpacingRatio(buy, s.regimeCfg.TrendUpBuySpacingMult)
		case RegimeTrendDown:
			buy = scaleSpacingRatio(buy, s.regimeCfg.TrendDownBuySpacingMult)
			sell = scaleSpacingRatio(sell, s.regimeCfg.TrendDownSellSpacingMult)
		}
	}
	if buy.Cmp(one) <= 0 {
		buy = s.Ratio
	}
	if sell.Cmp(one) <= 0 {
		sell = s.SellRatio
	}
	return buy, sell
}

func scaleSpacingRatio(base decimal.Decimal, mult float64) decimal.Decimal {
	if base.Cmp(decimal.NewFromInt(1)) <= 0 || mult <= 0 {
		return base
	}
	gap := base.Sub(decimal.NewFromInt(1))
	return decimal.NewFromInt(1).Add(gap.Mul(decimal.NewFromFloat(mult)))
}

func (s *SpotDual) priceForLevel(idx int) decimal.Decimal {
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

func (s *SpotDual) indexForPrice(price decimal.Decimal) (int, bool) {
	if s.anchor.Cmp(decimal.Zero) <= 0 {
		return 0, false
	}
	target := price
	if s.rules.PriceTick.Cmp(decimal.Zero) > 0 {
		target = core.RoundDown(price, s.rules.PriceTick)
	}

	minIdx := s.minLevel
	maxIdx := s.maxLevel
	if maxIdx == 0 {
		maxIdx = s.sellLevels()
	}
	if minIdx == 0 && maxIdx <= s.Levels {
		minIdx = -s.Levels
	}
	for idx := minIdx; idx <= maxIdx; idx++ {
		if s.priceForLevel(idx).Cmp(target) == 0 {
			return idx, true
		}
	}
	return 0, false
}

func (s *SpotDual) placeLimit(ctx context.Context, side core.Side, idx int) error {
	if idx > s.maxLevel {
		return nil
	}
	if s.hasOrderLevel(idx) {
		return nil
	}
	price := s.priceForLevel(idx)
	if price.Cmp(decimal.Zero) <= 0 {
		return nil
	}
	qty := s.orderQtyForSide(side)
	if qty.Cmp(decimal.Zero) <= 0 {
		return nil
	}
	order := core.Order{
		Symbol:    s.Symbol,
		Side:      side,
		Type:      core.Limit,
		Price:     price,
		Qty:       qty,
		GridIndex: idx,
		CreatedAt: time.Now().UTC(),
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
	placed.GridIndex = idx
	s.openOrders[placed.ID] = placed
	return nil
}

func (s *SpotDual) placeMarketBuy(ctx context.Context, qty decimal.Decimal) error {
	if qty.Cmp(decimal.Zero) <= 0 {
		return nil
	}
	order := core.Order{
		Symbol:    s.Symbol,
		Side:      core.Buy,
		Type:      core.Market,
		Qty:       qty,
		Price:     s.anchor,
		CreatedAt: time.Now().UTC(),
	}
	norm, err := core.NormalizeOrder(order, s.rules)
	if err != nil {
		return err
	}
	order = norm
	placed, err := s.executor.PlaceOrder(ctx, order)
	if err != nil {
		return err
	}
	if placed.ID != "" {
		s.ignoreFills[placed.ID] = struct{}{}
	}
	return nil
}

func (s *SpotDual) extendDown(ctx context.Context) error {
	if s.Levels <= 0 {
		return nil
	}
	oldMin := s.minLevel
	s.minLevel = s.minLevel - s.Levels
	for i := oldMin - 1; i >= s.minLevel; i-- {
		if err := s.placeLimit(ctx, core.Buy, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *SpotDual) shiftUp(ctx context.Context, filledLevel int) error {
	shift := s.shiftLevels()
	if shift < 1 {
		return nil
	}
	oldMin := s.minLevel
	oldMax := s.maxLevel
	if filledLevel != oldMax {
		return nil
	}
	newMin := oldMin + shift
	newMax := oldMax + shift
	if err := s.cancelBuyRange(ctx, oldMin, oldMin+shift-1); err != nil {
		return err
	}
	if err := s.placeLimit(ctx, core.Buy, oldMax); err != nil {
		return err
	}
	buyQty, err := s.shiftBuyNeed(ctx, shift)
	if err != nil {
		return err
	}
	if buyQty.Cmp(decimal.Zero) > 0 {
		if err := s.placeMarketBuy(ctx, buyQty); err != nil {
			return err
		}
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

func (s *SpotDual) updateRegime(price decimal.Decimal) bool {
	return s.updateRegimeAt(price, time.Now().UTC())
}

func (s *SpotDual) updateRegimeAt(price decimal.Decimal, at time.Time) bool {
	if s.regime == nil || !s.regimeCfg.Enabled {
		return false
	}
	px := price.InexactFloat64()
	if px <= 0 || math.IsNaN(px) || math.IsInf(px, 0) {
		return false
	}
	now := at.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	prev := s.regimeState
	next, changed, score := s.regime.Update(px, now)
	s.regimeState = next
	if changed {
		s.alertImportant("regime_changed", map[string]string{
			"from":  string(prev),
			"to":    string(next),
			"score": strconv.FormatFloat(score, 'f', 4, 64),
			"price": price.String(),
		})
	}
	return changed
}

func (s *SpotDual) rebuildOrders(ctx context.Context, price decimal.Decimal) error {
	if err := s.cancelAllOpenOrders(ctx); err != nil {
		return err
	}
	s.openOrders = make(map[string]core.Order)
	if price.Cmp(decimal.Zero) > 0 {
		s.anchor = price
	}
	s.maxLevel = s.sellLevels()
	s.minLevel = -s.Levels
	for i := 1; i <= s.maxLevel; i++ {
		if err := s.placeLimit(ctx, core.Sell, i); err != nil {
			if isInsufficientBalanceError(err) {
				s.alertImportant("rebuild_order_skipped_insufficient_balance", map[string]string{
					"stage": "place_sell",
					"level": strconv.Itoa(i),
					"err":   err.Error(),
				})
				break
			}
			return err
		}
	}
	for i := -1; i >= s.minLevel; i-- {
		if err := s.placeLimit(ctx, core.Buy, i); err != nil {
			if isInsufficientBalanceError(err) {
				s.alertImportant("rebuild_order_skipped_insufficient_balance", map[string]string{
					"stage": "place_buy",
					"level": strconv.Itoa(i),
					"err":   err.Error(),
				})
				break
			}
			return err
		}
	}
	return nil
}

func (s *SpotDual) shouldStop(price decimal.Decimal) bool {
	if s.StopPrice.Cmp(decimal.Zero) <= 0 {
		return false
	}
	if price.Cmp(decimal.Zero) <= 0 {
		return false
	}
	return price.Cmp(s.StopPrice) > 0
}

func (s *SpotDual) stopNow(ctx context.Context) error {
	if err := s.cancelAllOpenOrders(ctx); err != nil {
		return err
	}
	s.stopped = true
	s.initialized = false
	s.alertImportant("strategy_stop_price_triggered", map[string]string{
		"symbol":     s.Symbol,
		"stop_price": s.StopPrice.String(),
	})
	if err := s.persistSnapshot(); err != nil {
		return err
	}
	return ErrStopped
}

func (s *SpotDual) cancelAllOpenOrders(ctx context.Context) error {
	var firstErr error
	for id, ord := range s.openOrders {
		if id == "" {
			delete(s.openOrders, id)
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

func (s *SpotDual) cancelBuyRange(ctx context.Context, from, to int) error {
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

func (s *SpotDual) shiftLevels() int {
	if s.Shift > 0 {
		return s.Shift
	}
	if s.Levels < 2 {
		return 1
	}
	return s.Levels / 2
}

func (s *SpotDual) sellLevels() int {
	n := s.shiftLevels()
	if n < 1 {
		return 1
	}
	return n
}

func (s *SpotDual) shiftBuyNeed(ctx context.Context, shift int) (decimal.Decimal, error) {
	if shift < 1 {
		return decimal.Zero, nil
	}
	required := s.orderQty().Mul(decimal.NewFromInt(int64(shift)))
	if required.Cmp(decimal.Zero) <= 0 {
		return decimal.Zero, nil
	}
	bal, err := s.executor.Balances(ctx)
	if err != nil {
		return decimal.Zero, err
	}
	freeBase := bal.Base.Sub(s.lockedSellBase())
	if freeBase.Cmp(decimal.Zero) < 0 {
		freeBase = decimal.Zero
	}
	need := required.Sub(freeBase)
	if need.Cmp(decimal.Zero) <= 0 {
		return decimal.Zero, nil
	}
	return s.roundQtyUp(need), nil
}

func (s *SpotDual) lockedSellBase() decimal.Decimal {
	total := decimal.Zero
	for _, ord := range s.openOrders {
		if ord.Side != core.Sell {
			continue
		}
		total = total.Add(ord.Qty)
	}
	return total
}

func (s *SpotDual) roundQtyUp(qty decimal.Decimal) decimal.Decimal {
	if qty.Cmp(decimal.Zero) <= 0 {
		return decimal.Zero
	}
	out := qty
	if s.rules.QtyStep.Cmp(decimal.Zero) > 0 {
		out = out.Div(s.rules.QtyStep).Ceil().Mul(s.rules.QtyStep)
	}
	if s.rules.MinQty.Cmp(decimal.Zero) > 0 && out.Cmp(s.rules.MinQty) < 0 {
		out = s.rules.MinQty
	}
	return out
}

func (s *SpotDual) hasOrderLevel(idx int) bool {
	for _, ord := range s.openOrders {
		if ord.GridIndex == idx {
			return true
		}
	}
	return false
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
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "insufficient quote balance") || strings.Contains(msg, "insufficient base balance")
}

func (s *SpotDual) persistSnapshot() error {
	if s.store == nil {
		return nil
	}
	state := s.snapshotState()
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

func (s *SpotDual) alertImportant(event string, fields map[string]string) {
	if s.alerter == nil {
		return
	}
	s.alerter.Important(event, fields)
}

func (s *SpotDual) snapshotState() store.GridState {
	state := store.GridState{
		Strategy:       "spot_dual",
		Symbol:         s.Symbol,
		Anchor:         s.anchor,
		StopPrice:      s.StopPrice,
		Ratio:          s.Ratio,
		SellRatio:      s.SellRatio,
		Levels:         s.Levels,
		MinLevel:       s.minLevel,
		MaxLevel:       s.maxLevel,
		Qty:            s.Qty,
		MinQtyMultiple: s.minQtyMultiple,
		Rules:          s.rules,
		Initialized:    s.initialized,
		Stopped:        s.stopped,
		Regime:         string(s.regimeState),
	}
	if s.minLevel != 0 {
		state.Low = s.priceForLevel(s.minLevel)
	}
	return state
}

func (s *SpotDual) snapshotOrders() []core.Order {
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
