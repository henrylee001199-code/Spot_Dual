package core

import (
	"time"

	"github.com/shopspring/decimal"
)

type Side string

type OrderType string

type OrderStatus string

const (
	Buy  Side = "BUY"
	Sell Side = "SELL"
)

const (
	Limit  OrderType = "LIMIT"
	Market OrderType = "MARKET"
)

const (
	OrderNew             OrderStatus = "NEW"
	OrderPartiallyFilled OrderStatus = "PARTIALLY_FILLED"
	OrderFilled          OrderStatus = "FILLED"
	OrderCanceled        OrderStatus = "CANCELED"
	OrderRejected        OrderStatus = "REJECTED"
	OrderExpired         OrderStatus = "EXPIRED"
)

type Order struct {
	ID        string
	ClientID  string
	Symbol    string
	Side      Side
	Type      OrderType
	Price     decimal.Decimal
	Qty       decimal.Decimal
	Status    OrderStatus
	CreatedAt time.Time
	FilledAt  *time.Time
	GridIndex int
}

type Trade struct {
	OrderID  string
	TradeID  string
	ClientID string
	Symbol   string
	Side     Side
	Price    decimal.Decimal
	Qty      decimal.Decimal
	Status   OrderStatus
	Time     time.Time
}

type Rules struct {
	MinQty      decimal.Decimal
	MinNotional decimal.Decimal
	PriceTick   decimal.Decimal
	QtyStep     decimal.Decimal
}

type Balance struct {
	Base        decimal.Decimal
	Quote       decimal.Decimal
	BaseFree    decimal.Decimal
	BaseLocked  decimal.Decimal
	QuoteFree   decimal.Decimal
	QuoteLocked decimal.Decimal
}
