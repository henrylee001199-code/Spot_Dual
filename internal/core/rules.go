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
		order.Qty = rules.MinQty
	}
	if order.Type == Market {
		if order.Price.Cmp(decimal.Zero) <= 0 {
			return order, nil
		}
		order.Qty = ensureMinNotionalQty(order.Price, order.Qty, rules)
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
	order.Qty = ensureMinNotionalQty(order.Price, order.Qty, rules)
	return order, nil
}

func ensureMinNotionalQty(price, qty decimal.Decimal, rules Rules) decimal.Decimal {
	out := qty
	if rules.MinNotional.Cmp(decimal.Zero) > 0 && price.Cmp(decimal.Zero) > 0 {
		notional := price.Mul(out)
		if notional.Cmp(rules.MinNotional) < 0 {
			minQtyForNotional := rules.MinNotional.Div(price)
			if minQtyForNotional.Cmp(out) > 0 {
				out = minQtyForNotional
			}
		}
	}
	if rules.MinQty.Cmp(decimal.Zero) > 0 && out.Cmp(rules.MinQty) < 0 {
		out = rules.MinQty
	}
	if rules.QtyStep.Cmp(decimal.Zero) > 0 {
		out = roundUp(out, rules.QtyStep)
	}
	return out
}

func roundUp(value, step decimal.Decimal) decimal.Decimal {
	if step.Cmp(decimal.Zero) <= 0 {
		return value
	}
	return value.Div(step).Ceil().Mul(step)
}

func RoundDown(value, step decimal.Decimal) decimal.Decimal {
	if step.Cmp(decimal.Zero) <= 0 {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}
