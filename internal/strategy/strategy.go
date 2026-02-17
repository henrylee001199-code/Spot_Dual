package strategy

import (
	"context"
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"grid-trading/internal/core"
)

type Strategy interface {
	Init(ctx context.Context, price decimal.Decimal) error
	OnFill(ctx context.Context, trade core.Trade) error
}

var ErrStopped = errors.New("strategy stopped")

type Resetter interface {
	Reset()
}

type Reconciler interface {
	Reconcile(ctx context.Context, price decimal.Decimal, openOrders []core.Order) error
}

type TickAware interface {
	OnTick(ctx context.Context, price decimal.Decimal, at time.Time) error
}
