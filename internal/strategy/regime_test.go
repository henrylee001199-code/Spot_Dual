package strategy

import (
	"testing"
	"time"
)

func TestRegimeDetectorSwitchesToTrendUp(t *testing.T) {
	d := newRegimeDetector(RegimeControlConfig{
		Enabled:      true,
		Window:       5,
		EnterScore:   0.5,
		ExitScore:    0.2,
		EnterConfirm: 1,
		ExitConfirm:  1,
	})
	now := time.Now().UTC()
	prices := []float64{100, 101, 102, 103, 104, 105}

	state := RegimeRange
	for _, p := range prices {
		var changed bool
		state, changed, _ = d.Update(p, now)
		now = now.Add(time.Second)
		if changed {
			break
		}
	}
	if state != RegimeTrendUp {
		t.Fatalf("state = %s, want %s", state, RegimeTrendUp)
	}
}

func TestRegimeDetectorReturnsToRange(t *testing.T) {
	d := newRegimeDetector(RegimeControlConfig{
		Enabled:      true,
		Window:       5,
		EnterScore:   0.5,
		ExitScore:    0.2,
		EnterConfirm: 1,
		ExitConfirm:  1,
	})
	now := time.Now().UTC()
	for _, p := range []float64{100, 101, 102, 103, 104, 105} {
		d.Update(p, now)
		now = now.Add(time.Second)
	}
	if d.state != RegimeTrendUp {
		t.Fatalf("state = %s, want %s before exit", d.state, RegimeTrendUp)
	}

	for _, p := range []float64{105, 105, 105, 105, 105, 105} {
		d.Update(p, now)
		now = now.Add(time.Second)
	}
	if d.state != RegimeRange {
		t.Fatalf("state = %s, want %s", d.state, RegimeRange)
	}
}
