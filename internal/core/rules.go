package core

import (
	"errors"

	"github.com/shopspring/decimal"
)

var (
	ErrInvalidOrder     = errors.New("invalid order")
	ErrBelowMinQty      = errors.New("qty below min")
	ErrBelowMinNotional = errors.New("notional below min")
)

func NormalizeOrder(order Order, rules Rules) (Order, error) {
	if order.Qty.Cmp(decimal.Zero) <= 0 {
		return order, ErrInvalidOrder
	}
	if rules.QtyStep.Cmp(decimal.Zero) > 0 {
		order.Qty = RoundDown(order.Qty, rules.QtyStep)
	}
	if order.Qty.Cmp(decimal.Zero) <= 0 {
		return order, ErrInvalidOrder
	}
	if rules.MinQty.Cmp(decimal.Zero) > 0 && order.Qty.Cmp(rules.MinQty) < 0 {
		return order, ErrBelowMinQty
	}
	if order.Type == Market {
		if order.Price.Cmp(decimal.Zero) <= 0 {
			return order, nil
		}
		if rules.MinNotional.Cmp(decimal.Zero) > 0 {
			notional := order.Price.Mul(order.Qty)
			if notional.Cmp(rules.MinNotional) < 0 {
				return order, ErrBelowMinNotional
			}
		}
		return order, nil
	}
	if order.Price.Cmp(decimal.Zero) <= 0 {
		return order, ErrInvalidOrder
	}
	if rules.PriceTick.Cmp(decimal.Zero) > 0 {
		order.Price = RoundDown(order.Price, rules.PriceTick)
	}
	if order.Price.Cmp(decimal.Zero) <= 0 {
		return order, ErrInvalidOrder
	}
	if rules.MinNotional.Cmp(decimal.Zero) > 0 {
		notional := order.Price.Mul(order.Qty)
		if notional.Cmp(rules.MinNotional) < 0 {
			return order, ErrBelowMinNotional
		}
	}
	return order, nil
}

func RoundDown(value, step decimal.Decimal) decimal.Decimal {
	if step.Cmp(decimal.Zero) <= 0 {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}
