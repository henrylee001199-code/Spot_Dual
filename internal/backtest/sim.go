package backtest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"grid-trading/internal/core"
)

type SimExchange struct {
	symbol        string
	rules         core.Rules
	walletQuote   decimal.Decimal
	positionQty   decimal.Decimal
	positionEntry decimal.Decimal
	openOrders    map[string]*core.Order
	orderSeq      int
	lastPrice     decimal.Decimal
	makerFee      decimal.Decimal
	takerFee      decimal.Decimal
	feePaid       decimal.Decimal
	marketBuyN    int
	marketBuyQ    decimal.Decimal
}

func NewSimExchange(symbol string, balance core.Balance, rules core.Rules) *SimExchange {
	return &SimExchange{
		symbol:      symbol,
		rules:       rules,
		walletQuote: balance.Quote,
		positionQty: balance.Base,
		openOrders:  make(map[string]*core.Order),
		lastPrice:   decimal.Zero,
		makerFee:    decimal.Zero,
		takerFee:    decimal.Zero,
		feePaid:     decimal.Zero,
		marketBuyQ:  decimal.Zero,
	}
}

func (s *SimExchange) SetFees(makerRate, takerRate decimal.Decimal) error {
	if makerRate.Cmp(decimal.Zero) < 0 || takerRate.Cmp(decimal.Zero) < 0 {
		return errors.New("fee rate must be >= 0")
	}
	s.makerFee = makerRate
	s.takerFee = takerRate
	return nil
}

type Snapshot struct {
	FreeBase      decimal.Decimal
	FreeQuote     decimal.Decimal
	LockedBase    decimal.Decimal
	LockedQuote   decimal.Decimal
	TotalBase     decimal.Decimal
	TotalQuote    decimal.Decimal
	EquityQuote   decimal.Decimal
	LockedCapital decimal.Decimal
	FeePaidQuote  decimal.Decimal
}

func (s *SimExchange) Snapshot(price decimal.Decimal) Snapshot {
	if price.Cmp(decimal.Zero) <= 0 {
		price = s.lastPrice
	}
	unrealized := s.unrealizedPnL(price)
	equity := s.walletQuote.Add(unrealized)
	lockedCapital := s.positionQty.Abs().Mul(price)
	return Snapshot{
		FreeBase:      s.positionQty,
		FreeQuote:     s.walletQuote,
		LockedBase:    decimal.Zero,
		LockedQuote:   decimal.Zero,
		TotalBase:     s.positionQty,
		TotalQuote:    s.walletQuote,
		EquityQuote:   equity,
		LockedCapital: lockedCapital,
		FeePaidQuote:  s.feePaid,
	}
}

func (s *SimExchange) Name() string { return "backtest" }

func (s *SimExchange) GetRules(ctx context.Context, symbol string) (core.Rules, error) {
	if symbol != s.symbol {
		return core.Rules{}, errors.New("unknown symbol")
	}
	return s.rules, nil
}

func (s *SimExchange) PlaceOrder(ctx context.Context, order core.Order) (core.Order, error) {
	if order.Symbol != s.symbol {
		return core.Order{}, errors.New("unknown symbol")
	}
	if order.Qty.Cmp(decimal.Zero) <= 0 {
		return core.Order{}, errors.New("invalid order")
	}
	price := order.Price
	if order.Type == core.Market && s.lastPrice.Cmp(decimal.Zero) > 0 {
		price = s.lastPrice
	}
	if price.Cmp(decimal.Zero) <= 0 {
		return core.Order{}, errors.New("invalid order price")
	}
	order.Price = price
	s.orderSeq++
	order.ID = fmt.Sprintf("bt-%d", s.orderSeq)
	order.CreatedAt = time.Now()
	if order.Type == core.Market {
		s.applyFill(order.Side, order.Qty, order.Price, s.takerFee)
		order.Status = core.OrderFilled
		filledAt := time.Now()
		order.FilledAt = &filledAt
		if order.Side == core.Buy {
			s.marketBuyN++
			s.marketBuyQ = s.marketBuyQ.Add(order.Qty)
		}
		return order, nil
	}
	order.Status = core.OrderNew
	s.openOrders[order.ID] = &order
	return order, nil
}

func (s *SimExchange) CancelOrder(ctx context.Context, symbol, orderID string) error {
	if symbol != s.symbol {
		return errors.New("unknown symbol")
	}
	ord, ok := s.openOrders[orderID]
	if !ok {
		return errors.New("order not found")
	}
	ord.Status = core.OrderCanceled
	delete(s.openOrders, orderID)
	return nil
}

func (s *SimExchange) OpenOrders(ctx context.Context, symbol string) ([]core.Order, error) {
	if symbol != s.symbol {
		return nil, errors.New("unknown symbol")
	}
	orders := make([]core.Order, 0, len(s.openOrders))
	for _, ord := range s.openOrders {
		orders = append(orders, *ord)
	}
	return orders, nil
}

func (s *SimExchange) Balances(ctx context.Context) (core.Balance, error) {
	equity := s.walletQuote
	if s.lastPrice.Cmp(decimal.Zero) > 0 {
		equity = s.walletQuote.Add(s.unrealizedPnL(s.lastPrice))
	}
	return core.Balance{
		Base:  s.positionQty,
		Quote: equity,
	}, nil
}

func (s *SimExchange) FreeBalances() core.Balance {
	return core.Balance{Base: s.positionQty, Quote: s.walletQuote}
}

func (s *SimExchange) MarketBuyStats() (int, decimal.Decimal) {
	return s.marketBuyN, s.marketBuyQ
}

func (s *SimExchange) Match(price decimal.Decimal, ts time.Time) []core.Trade {
	s.lastPrice = price
	trades := make([]core.Trade, 0)
	for id, ord := range s.openOrders {
		if !shouldFill(ord, price) {
			continue
		}
		trade := core.Trade{
			OrderID:      ord.ID,
			Symbol:       ord.Symbol,
			Side:         ord.Side,
			PositionSide: ord.PositionSide,
			ReduceOnly:   ord.ReduceOnly,
			Price:        ord.Price,
			Qty:          ord.Qty,
			Status:       core.OrderFilled,
			Time:         ts,
		}
		s.applyFill(ord.Side, ord.Qty, ord.Price, s.makerFee)
		ord.Status = core.OrderFilled
		filledAt := ts
		ord.FilledAt = &filledAt
		delete(s.openOrders, id)
		trades = append(trades, trade)
	}
	return trades
}

func shouldFill(ord *core.Order, price decimal.Decimal) bool {
	switch ord.Side {
	case core.Buy:
		return price.Cmp(ord.Price) <= 0
	case core.Sell:
		return price.Cmp(ord.Price) >= 0
	default:
		return false
	}
}

func (s *SimExchange) applyFill(side core.Side, qty, price, feeRate decimal.Decimal) {
	if qty.Cmp(decimal.Zero) <= 0 || price.Cmp(decimal.Zero) <= 0 {
		return
	}
	notional := price.Mul(qty)
	fee := notional.Mul(feeRate)
	s.walletQuote = s.walletQuote.Sub(fee)
	s.feePaid = s.feePaid.Add(fee)

	switch side {
	case core.Buy:
		s.applyBuyFill(qty, price)
	case core.Sell:
		s.applySellFill(qty, price)
	}
}

func (s *SimExchange) applyBuyFill(qty, price decimal.Decimal) {
	if s.positionQty.Cmp(decimal.Zero) >= 0 {
		oldQty := s.positionQty
		newQty := oldQty.Add(qty)
		if oldQty.Cmp(decimal.Zero) <= 0 {
			s.positionEntry = price
		} else {
			s.positionEntry = weightedPrice(s.positionEntry, oldQty, price, qty)
		}
		s.positionQty = newQty
		return
	}

	shortQty := s.positionQty.Abs()
	closeQty := minDecimal(qty, shortQty)
	if closeQty.Cmp(decimal.Zero) > 0 {
		realized := s.positionEntry.Sub(price).Mul(closeQty)
		s.walletQuote = s.walletQuote.Add(realized)
		s.positionQty = s.positionQty.Add(closeQty)
	}
	remain := qty.Sub(closeQty)
	if s.positionQty.Cmp(decimal.Zero) == 0 {
		s.positionEntry = decimal.Zero
	}
	if remain.Cmp(decimal.Zero) > 0 {
		s.positionEntry = price
		s.positionQty = s.positionQty.Add(remain)
	}
}

func (s *SimExchange) applySellFill(qty, price decimal.Decimal) {
	if s.positionQty.Cmp(decimal.Zero) <= 0 {
		oldAbs := s.positionQty.Abs()
		newAbs := oldAbs.Add(qty)
		if oldAbs.Cmp(decimal.Zero) <= 0 {
			s.positionEntry = price
		} else {
			s.positionEntry = weightedPrice(s.positionEntry, oldAbs, price, qty)
		}
		s.positionQty = newAbs.Neg()
		return
	}

	longQty := s.positionQty
	closeQty := minDecimal(qty, longQty)
	if closeQty.Cmp(decimal.Zero) > 0 {
		realized := price.Sub(s.positionEntry).Mul(closeQty)
		s.walletQuote = s.walletQuote.Add(realized)
		s.positionQty = s.positionQty.Sub(closeQty)
	}
	remain := qty.Sub(closeQty)
	if s.positionQty.Cmp(decimal.Zero) == 0 {
		s.positionEntry = decimal.Zero
	}
	if remain.Cmp(decimal.Zero) > 0 {
		s.positionEntry = price
		s.positionQty = s.positionQty.Sub(remain)
	}
}

func (s *SimExchange) unrealizedPnL(markPrice decimal.Decimal) decimal.Decimal {
	if markPrice.Cmp(decimal.Zero) <= 0 || s.positionQty.Cmp(decimal.Zero) == 0 {
		return decimal.Zero
	}
	if s.positionQty.Cmp(decimal.Zero) > 0 {
		return markPrice.Sub(s.positionEntry).Mul(s.positionQty)
	}
	return s.positionEntry.Sub(markPrice).Mul(s.positionQty.Abs())
}

func weightedPrice(p1, q1, p2, q2 decimal.Decimal) decimal.Decimal {
	total := q1.Add(q2)
	if total.Cmp(decimal.Zero) <= 0 {
		return decimal.Zero
	}
	return p1.Mul(q1).Add(p2.Mul(q2)).Div(total)
}

func minDecimal(a, b decimal.Decimal) decimal.Decimal {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}
