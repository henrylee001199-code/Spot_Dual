package strategy

import (
	"math"
	"time"
)

type RegimeState string

const (
	RegimeRange     RegimeState = "range"
	RegimeTrendUp   RegimeState = "trend_up"
	RegimeTrendDown RegimeState = "trend_down"
)

type RegimeControlConfig struct {
	Enabled                  bool
	Window                   int
	EnterScore               float64
	ExitScore                float64
	EnterConfirm             int
	ExitConfirm              int
	MinDwell                 time.Duration
	TrendUpBuySpacingMult    float64
	TrendDownBuySpacingMult  float64
	TrendDownSellSpacingMult float64
	TrendUpSellQtyFactor     float64
}

type regimeDetector struct {
	cfg        RegimeControlConfig
	state      RegimeState
	lastChange time.Time
	enterHits  int
	exitHits   int
	prices     []float64
}

func normalizeRegimeConfig(cfg RegimeControlConfig) RegimeControlConfig {
	if cfg.Window < 5 {
		cfg.Window = 30
	}
	if cfg.EnterScore <= 0 {
		cfg.EnterScore = 1.8
	}
	if cfg.ExitScore <= 0 {
		cfg.ExitScore = 1.2
	}
	if cfg.EnterScore < cfg.ExitScore {
		cfg.EnterScore = cfg.ExitScore + 0.1
	}
	if cfg.EnterConfirm < 1 {
		cfg.EnterConfirm = 3
	}
	if cfg.ExitConfirm < 1 {
		cfg.ExitConfirm = 5
	}
	if cfg.MinDwell < 0 {
		cfg.MinDwell = 0
	}
	if cfg.TrendUpBuySpacingMult <= 0 {
		cfg.TrendUpBuySpacingMult = 0.7
	}
	if cfg.TrendDownBuySpacingMult <= 0 {
		cfg.TrendDownBuySpacingMult = 1.6
	}
	if cfg.TrendDownSellSpacingMult <= 0 {
		cfg.TrendDownSellSpacingMult = 0.7
	}
	if cfg.TrendUpSellQtyFactor <= 0 || cfg.TrendUpSellQtyFactor > 1 {
		cfg.TrendUpSellQtyFactor = 0.5
	}
	return cfg
}

func newRegimeDetector(cfg RegimeControlConfig) *regimeDetector {
	cfg = normalizeRegimeConfig(cfg)
	return &regimeDetector{
		cfg:    cfg,
		state:  RegimeRange,
		prices: make([]float64, 0, cfg.Window),
	}
}

func (d *regimeDetector) Update(price float64, now time.Time) (RegimeState, bool, float64) {
	if d == nil || !d.cfg.Enabled {
		return RegimeRange, false, 0
	}
	if price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return d.state, false, 0
	}
	if len(d.prices) == d.cfg.Window {
		copy(d.prices, d.prices[1:])
		d.prices[len(d.prices)-1] = price
	} else {
		d.prices = append(d.prices, price)
	}
	if len(d.prices) < d.cfg.Window {
		return d.state, false, 0
	}

	score, direction := trendScore(d.prices)
	canSwitch := d.lastChange.IsZero() || now.Sub(d.lastChange) >= d.cfg.MinDwell
	next := d.state
	switch d.state {
	case RegimeRange:
		if score >= d.cfg.EnterScore && direction != 0 {
			d.enterHits++
			d.exitHits = 0
			if d.enterHits >= d.cfg.EnterConfirm && canSwitch {
				if direction > 0 {
					next = RegimeTrendUp
				} else {
					next = RegimeTrendDown
				}
			}
		} else {
			d.enterHits = 0
			d.exitHits = 0
		}
	case RegimeTrendUp, RegimeTrendDown:
		if score <= d.cfg.ExitScore {
			d.exitHits++
			d.enterHits = 0
			if d.exitHits >= d.cfg.ExitConfirm && canSwitch {
				next = RegimeRange
			}
		} else if score >= d.cfg.EnterScore && direction != 0 {
			want := RegimeTrendUp
			if direction < 0 {
				want = RegimeTrendDown
			}
			if want != d.state {
				d.enterHits++
				d.exitHits = 0
				if d.enterHits >= d.cfg.EnterConfirm && canSwitch {
					next = want
				}
			} else {
				d.enterHits = 0
				d.exitHits = 0
			}
		} else {
			d.enterHits = 0
			d.exitHits = 0
		}
	}

	if next != d.state {
		d.state = next
		d.lastChange = now
		d.enterHits = 0
		d.exitHits = 0
		return d.state, true, score
	}
	return d.state, false, score
}

func trendScore(prices []float64) (score float64, direction int) {
	if len(prices) < 3 {
		return 0, 0
	}
	first := prices[0]
	last := prices[len(prices)-1]
	if first <= 0 || last <= 0 {
		return 0, 0
	}
	ret := math.Log(last / first)
	if ret > 0 {
		direction = 1
	} else if ret < 0 {
		direction = -1
	}
	if direction == 0 {
		return 0, 0
	}

	sum := 0.0
	sumSq := 0.0
	n := 0
	for i := 1; i < len(prices); i++ {
		p0 := prices[i-1]
		p1 := prices[i]
		if p0 <= 0 || p1 <= 0 {
			continue
		}
		r := math.Log(p1 / p0)
		sum += r
		sumSq += r * r
		n++
	}
	if n < 2 {
		return 0, direction
	}
	mean := sum / float64(n)
	variance := sumSq/float64(n) - mean*mean
	if variance < 0 {
		variance = 0
	}
	vol := math.Sqrt(variance)
	if vol == 0 {
		return 999, direction
	}
	return math.Abs(ret) / vol, direction
}
