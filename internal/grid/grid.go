package grid

import (
	"errors"
	"math"
	"sort"

	"github.com/shopspring/decimal"
)

type Mode string

const (
	ModeGeometric Mode = "geometric"
)

type Spec struct {
	Low    decimal.Decimal
	High   decimal.Decimal
	Levels int
	Mode   Mode
}

type Grid struct {
	Prices []decimal.Decimal
}

func Build(spec Spec) (Grid, error) {
	if spec.Levels < 1 {
		return Grid{}, errors.New("levels must be >= 1")
	}
	if spec.Low.Cmp(decimal.Zero) <= 0 || spec.High.Cmp(decimal.Zero) <= 0 || spec.High.Cmp(spec.Low) <= 0 {
		return Grid{}, errors.New("invalid price range")
	}
	if spec.Mode == "" {
		spec.Mode = ModeGeometric
	}
	if spec.Mode != ModeGeometric {
		return Grid{}, errors.New("only geometric grid mode is supported")
	}
	prices := make([]decimal.Decimal, spec.Levels+1)
	ratio := math.Pow(spec.High.Div(spec.Low).InexactFloat64(), 1/float64(spec.Levels))
	for i := 0; i <= spec.Levels; i++ {
		prices[i] = spec.Low.Mul(decimal.NewFromFloat(math.Pow(ratio, float64(i))))
	}
	return Grid{Prices: prices}, nil
}

func (g Grid) PriceAt(index int) decimal.Decimal {
	if index < 0 || index >= len(g.Prices) {
		return decimal.Zero
	}
	return g.Prices[index]
}

func (g Grid) IndexForPrice(price decimal.Decimal) int {
	idx := sort.Search(len(g.Prices), func(i int) bool { return g.Prices[i].Cmp(price) > 0 }) - 1
	if idx < 0 {
		return 0
	}
	if idx >= len(g.Prices) {
		return len(g.Prices) - 1
	}
	return idx
}

func RoundDown(value, step decimal.Decimal) decimal.Decimal {
	if step.Cmp(decimal.Zero) <= 0 {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}

func Normalize(g Grid, tick decimal.Decimal) (Grid, error) {
	if tick.Cmp(decimal.Zero) <= 0 {
		return g, nil
	}
	out := make([]decimal.Decimal, 0, len(g.Prices))
	var last decimal.Decimal
	for _, p := range g.Prices {
		rp := RoundDown(p, tick)
		if len(out) == 0 || rp.Cmp(last) != 0 {
			out = append(out, rp)
			last = rp
		}
	}
	if len(out) < 2 {
		return Grid{}, errors.New("grid collapsed after tick normalization")
	}
	return Grid{Prices: out}, nil
}
