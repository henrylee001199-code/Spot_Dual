package config

import (
	"fmt"

	"github.com/shopspring/decimal"
	"gopkg.in/yaml.v3"
)

type Decimal struct {
	decimal.Decimal
}

func (d *Decimal) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("decimal must be a scalar")
	}
	if value.Value == "" {
		d.Decimal = decimal.Zero
		return nil
	}
	dec, err := decimal.NewFromString(value.Value)
	if err != nil {
		return fmt.Errorf("invalid decimal %q: %w", value.Value, err)
	}
	d.Decimal = dec
	return nil
}

func (d Decimal) MarshalYAML() (interface{}, error) {
	return d.String(), nil
}
