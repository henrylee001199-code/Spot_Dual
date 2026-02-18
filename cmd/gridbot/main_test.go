package main

import (
	"testing"

	"github.com/shopspring/decimal"

	"grid-trading/internal/config"
	"grid-trading/internal/core"
	"grid-trading/internal/strategy"
)

func ratioStepPtr(v string) *config.Decimal {
	return &config.Decimal{Decimal: decimal.RequireFromString(v)}
}

func TestApplySpotDualTuningKeepsDefaultRatioStepWhenUnset(t *testing.T) {
	strat := strategy.NewSpotDual(
		"BTCUSDT",
		decimal.Zero,
		decimal.RequireFromString("1.01"),
		20,
		10,
		decimal.RequireFromString("0.001"),
		1,
		core.Rules{},
		nil,
		nil,
	)
	want := strat.RatioStep
	wantQtyMultiple := strat.RatioQtyMultiple
	cfg := config.Config{
		Grid: config.GridConfig{
			SellRatio: config.Decimal{Decimal: decimal.RequireFromString("1.01")},
		},
	}

	applySpotDualTuning(strat, cfg)

	if !strat.RatioStep.Equal(want) {
		t.Fatalf("ratio_step changed unexpectedly: got=%s want=%s", strat.RatioStep.String(), want.String())
	}
	if !strat.RatioQtyMultiple.Equal(wantQtyMultiple) {
		t.Fatalf("ratio_qty_multiple changed unexpectedly: got=%s want=%s", strat.RatioQtyMultiple.String(), wantQtyMultiple.String())
	}
}

func TestApplySpotDualTuningAllowsZeroRatioStep(t *testing.T) {
	strat := strategy.NewSpotDual(
		"BTCUSDT",
		decimal.Zero,
		decimal.RequireFromString("1.01"),
		20,
		10,
		decimal.RequireFromString("0.001"),
		1,
		core.Rules{},
		nil,
		nil,
	)
	cfg := config.Config{
		Grid: config.GridConfig{
			SellRatio: config.Decimal{Decimal: decimal.RequireFromString("1.01")},
			RatioStep: ratioStepPtr("0"),
		},
	}

	applySpotDualTuning(strat, cfg)

	if !strat.RatioStep.Equal(decimal.Zero) {
		t.Fatalf("ratio_step = %s, want 0", strat.RatioStep.String())
	}
}

func TestApplySpotDualTuningSetsRatioQtyMultiple(t *testing.T) {
	strat := strategy.NewSpotDual(
		"BTCUSDT",
		decimal.Zero,
		decimal.RequireFromString("1.01"),
		20,
		10,
		decimal.RequireFromString("0.001"),
		1,
		core.Rules{},
		nil,
		nil,
	)
	cfg := config.Config{
		Grid: config.GridConfig{
			SellRatio:        config.Decimal{Decimal: decimal.RequireFromString("1.01")},
			RatioQtyMultiple: config.Decimal{Decimal: decimal.RequireFromString("1.2")},
		},
	}

	applySpotDualTuning(strat, cfg)

	if !strat.RatioQtyMultiple.Equal(decimal.RequireFromString("1.2")) {
		t.Fatalf("ratio_qty_multiple = %s, want 1.2", strat.RatioQtyMultiple.String())
	}
}
