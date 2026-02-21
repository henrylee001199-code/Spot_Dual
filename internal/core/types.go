package core

import (
	"time"

	"github.com/shopspring/decimal"
)

type Side string

type OrderType string

type OrderStatus string

type PositionSide string

const (
	Buy  Side = "BUY"
	Sell Side = "SELL"
)

const (
	PositionSideBoth  PositionSide = "BOTH"
	PositionSideLong  PositionSide = "LONG"
	PositionSideShort PositionSide = "SHORT"
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
	ID           string
	ClientID     string
	Symbol       string
	Side         Side
	Type         OrderType
	PositionSide PositionSide
	ReduceOnly   bool
	Price        decimal.Decimal
	Qty          decimal.Decimal
	Status       OrderStatus
	CreatedAt    time.Time
	FilledAt     *time.Time
	GridIndex    int
}

type Trade struct {
	OrderID      string
	TradeID      string
	Symbol       string
	Side         Side
	PositionSide PositionSide
	ReduceOnly   bool
	Price        decimal.Decimal
	Qty          decimal.Decimal
	Status       OrderStatus
	Time         time.Time
}

type Rules struct {
	MinQty      decimal.Decimal
	MinNotional decimal.Decimal
	PriceTick   decimal.Decimal
	QtyStep     decimal.Decimal
}

type Balance struct {
	Base  decimal.Decimal
	Quote decimal.Decimal
}
