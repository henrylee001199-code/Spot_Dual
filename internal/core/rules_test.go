package core

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestNormalizeOrderLimitRoundsPriceAndQty(t *testing.T) {
	order := Order{
		Symbol: "BTCUSDT",
		Side:   Buy,
		Type:   Limit,
		Price:  decimal.RequireFromString("100.037"),
		Qty:    decimal.RequireFromString("0.123456"),
	}
	rules := Rules{
		MinQty:      decimal.RequireFromString("0.01"),
		MinNotional: decimal.RequireFromString("10"),
		PriceTick:   decimal.RequireFromString("0.01"),
		QtyStep:     decimal.RequireFromString("0.001"),
	}

	got, err := NormalizeOrder(order, rules)
	if err != nil {
		t.Fatalf("NormalizeOrder() error = %v", err)
	}
	if !got.Price.Equal(decimal.RequireFromString("100.03")) {
		t.Fatalf("unexpected rounded price: %s", got.Price)
	}
	if !got.Qty.Equal(decimal.RequireFromString("0.123")) {
		t.Fatalf("unexpected rounded qty: %s", got.Qty)
	}
}

func TestNormalizeOrderBelowMinQty(t *testing.T) {
	order := Order{
		Symbol: "BTCUSDT",
		Side:   Buy,
		Type:   Limit,
		Price:  decimal.RequireFromString("100"),
		Qty:    decimal.RequireFromString("0.009"),
	}
	rules := Rules{
		MinQty: decimal.RequireFromString("0.01"),
	}

	_, err := NormalizeOrder(order, rules)
	if !errors.Is(err, ErrBelowMinQty) {
		t.Fatalf("NormalizeOrder() error = %v, want %v", err, ErrBelowMinQty)
	}
}

func TestNormalizeOrderLimitBelowMinNotional(t *testing.T) {
	order := Order{
		Symbol: "BTCUSDT",
		Side:   Buy,
		Type:   Limit,
		Price:  decimal.RequireFromString("100"),
		Qty:    decimal.RequireFromString("0.05"),
	}
	rules := Rules{
		MinNotional: decimal.RequireFromString("6"),
	}

	_, err := NormalizeOrder(order, rules)
	if !errors.Is(err, ErrBelowMinNotional) {
		t.Fatalf("NormalizeOrder() error = %v, want %v", err, ErrBelowMinNotional)
	}
}

func TestNormalizeOrderMarketMinNotionalRules(t *testing.T) {
	rules := Rules{
		MinNotional: decimal.RequireFromString("60"),
	}

	noPriceMarket := Order{
		Symbol: "BTCUSDT",
		Side:   Buy,
		Type:   Market,
		Price:  decimal.Zero,
		Qty:    decimal.RequireFromString("1"),
	}
	if _, err := NormalizeOrder(noPriceMarket, rules); err != nil {
		t.Fatalf("NormalizeOrder() no-price market error = %v", err)
	}

	withPriceMarket := Order{
		Symbol: "BTCUSDT",
		Side:   Buy,
		Type:   Market,
		Price:  decimal.RequireFromString("50"),
		Qty:    decimal.RequireFromString("1"),
	}
	if _, err := NormalizeOrder(withPriceMarket, rules); !errors.Is(err, ErrBelowMinNotional) {
		t.Fatalf("NormalizeOrder() market with price error = %v, want %v", err, ErrBelowMinNotional)
	}
}
