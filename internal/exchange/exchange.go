package exchange

import (
	"context"

	"grid-trading/internal/core"
)

type Exchange interface {
	Name() string
	GetRules(ctx context.Context, symbol string) (core.Rules, error)
	PlaceOrder(ctx context.Context, order core.Order) (core.Order, error)
	CancelOrder(ctx context.Context, symbol, orderID string) error
	OpenOrders(ctx context.Context, symbol string) ([]core.Order, error)
	Balances(ctx context.Context) (core.Balance, error)
}
