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
	cfg := config.Config{
		Grid: config.GridConfig{
			SellRatio: config.Decimal{Decimal: decimal.RequireFromString("1.01")},
		},
	}

	applySpotDualTuning(strat, cfg)

	if !strat.RatioStep.Equal(want) {
		t.Fatalf("ratio_step changed unexpectedly: got=%s want=%s", strat.RatioStep.String(), want.String())
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
