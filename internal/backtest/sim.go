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
	symbol      string
	rules       core.Rules
	balanceFree core.Balance
	baseLocked  decimal.Decimal
	quoteLocked decimal.Decimal
	openOrders  map[string]*core.Order
	orderSeq    int
	lastPrice   decimal.Decimal
	makerFee    decimal.Decimal
	takerFee    decimal.Decimal
	feePaid     decimal.Decimal
	marketBuyN  int
	marketBuyQ  decimal.Decimal
}

func NewSimExchange(symbol string, balance core.Balance, rules core.Rules) *SimExchange {
	return &SimExchange{
		symbol:      symbol,
		rules:       rules,
		balanceFree: balance,
		baseLocked:  decimal.Zero,
		quoteLocked: decimal.Zero,
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
	totalBase := s.balanceFree.Base.Add(s.baseLocked)
	totalQuote := s.balanceFree.Quote.Add(s.quoteLocked)
	lockedCapital := s.quoteLocked.Add(s.baseLocked.Mul(price))
	return Snapshot{
		FreeBase:      s.balanceFree.Base,
		FreeQuote:     s.balanceFree.Quote,
		LockedBase:    s.baseLocked,
		LockedQuote:   s.quoteLocked,
		TotalBase:     totalBase,
		TotalQuote:    totalQuote,
		EquityQuote:   totalQuote.Add(totalBase.Mul(price)),
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
		if err := s.applyMarketFill(&order); err != nil {
			return core.Order{}, err
		}
		order.Status = core.OrderFilled
		filledAt := time.Now()
		order.FilledAt = &filledAt
		return order, nil
	}
	switch order.Side {
	case core.Buy:
		reserve := s.reservedBuyQuote(order.Price, order.Qty, s.makerFee)
		if s.balanceFree.Quote.Cmp(reserve) < 0 {
			return core.Order{}, errors.New("insufficient quote balance")
		}
		s.balanceFree.Quote = s.balanceFree.Quote.Sub(reserve)
		s.quoteLocked = s.quoteLocked.Add(reserve)
	case core.Sell:
		if s.balanceFree.Base.Cmp(order.Qty) < 0 {
			return core.Order{}, errors.New("insufficient base balance")
		}
		s.balanceFree.Base = s.balanceFree.Base.Sub(order.Qty)
		s.baseLocked = s.baseLocked.Add(order.Qty)
	default:
		return core.Order{}, errors.New("unknown side")
	}
	order.Status = core.OrderNew
	s.openOrders[order.ID] = &order
	return order, nil
}

func (s *SimExchange) CancelOrder(ctx context.Context, symbol, orderID string) error {
	ord, ok := s.openOrders[orderID]
	if !ok {
		return errors.New("order not found")
	}
	if symbol != s.symbol {
		return errors.New("unknown symbol")
	}
	s.releaseLocked(ord)
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
	return core.Balance{
		Base:  s.balanceFree.Base.Add(s.baseLocked),
		Quote: s.balanceFree.Quote.Add(s.quoteLocked),
	}, nil
}

func (s *SimExchange) FreeBalances() core.Balance {
	return s.balanceFree
}

func (s *SimExchange) MarketBuyStats() (int, decimal.Decimal) {
	return s.marketBuyN, s.marketBuyQ
}

func (s *SimExchange) Match(price decimal.Decimal, ts time.Time) []core.Trade {
	s.lastPrice = price
	trades := make([]core.Trade, 0)
	for id, ord := range s.openOrders {
		if shouldFill(ord, price) {
			trade := core.Trade{
				OrderID: ord.ID,
				Symbol:  ord.Symbol,
				Side:    ord.Side,
				Price:   ord.Price,
				Qty:     ord.Qty,
				Status:  core.OrderFilled,
				Time:    ts,
			}
			s.applyLimitFill(ord)
			ord.Status = core.OrderFilled
			filledAt := ts
			ord.FilledAt = &filledAt
			delete(s.openOrders, id)
			trades = append(trades, trade)
		}
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

func (s *SimExchange) applyMarketFill(ord *core.Order) error {
	cost := ord.Price.Mul(ord.Qty)
	fee := cost.Mul(s.takerFee)
	switch ord.Side {
	case core.Buy:
		required := cost.Add(fee)
		if s.balanceFree.Quote.Cmp(required) < 0 {
			return errors.New("insufficient quote balance")
		}
		s.balanceFree.Quote = s.balanceFree.Quote.Sub(required)
		s.balanceFree.Base = s.balanceFree.Base.Add(ord.Qty)
		s.feePaid = s.feePaid.Add(fee)
		s.marketBuyN++
		s.marketBuyQ = s.marketBuyQ.Add(ord.Qty)
	case core.Sell:
		if s.balanceFree.Base.Cmp(ord.Qty) < 0 {
			return errors.New("insufficient base balance")
		}
		s.balanceFree.Base = s.balanceFree.Base.Sub(ord.Qty)
		s.balanceFree.Quote = s.balanceFree.Quote.Add(cost.Sub(fee))
		s.feePaid = s.feePaid.Add(fee)
	default:
		return errors.New("unknown side")
	}
	return nil
}

func (s *SimExchange) applyLimitFill(ord *core.Order) {
	cost := ord.Price.Mul(ord.Qty)
	fee := cost.Mul(s.makerFee)
	switch ord.Side {
	case core.Buy:
		reserve := s.reservedBuyQuote(ord.Price, ord.Qty, s.makerFee)
		s.quoteLocked = s.quoteLocked.Sub(reserve)
		s.balanceFree.Base = s.balanceFree.Base.Add(ord.Qty)
		s.feePaid = s.feePaid.Add(fee)
	case core.Sell:
		s.baseLocked = s.baseLocked.Sub(ord.Qty)
		s.balanceFree.Quote = s.balanceFree.Quote.Add(cost.Sub(fee))
		s.feePaid = s.feePaid.Add(fee)
	}
}

func (s *SimExchange) releaseLocked(ord *core.Order) {
	switch ord.Side {
	case core.Buy:
		reserve := s.reservedBuyQuote(ord.Price, ord.Qty, s.makerFee)
		s.quoteLocked = s.quoteLocked.Sub(reserve)
		s.balanceFree.Quote = s.balanceFree.Quote.Add(reserve)
	case core.Sell:
		s.baseLocked = s.baseLocked.Sub(ord.Qty)
		s.balanceFree.Base = s.balanceFree.Base.Add(ord.Qty)
	}
}

func (s *SimExchange) reservedBuyQuote(price, qty, feeRate decimal.Decimal) decimal.Decimal {
	cost := price.Mul(qty)
	if feeRate.Cmp(decimal.Zero) <= 0 {
		return cost
	}
	return cost.Mul(decimal.NewFromInt(1).Add(feeRate))
}
