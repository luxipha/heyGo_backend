package statutory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
)

var ErrInvalidRule = errors.New("invalid statutory charge rule")

type Rule struct {
	Type    string
	Base    string
	Config  json.RawMessage
	Minimum *int64
	Maximum *int64
}

// Calculate returns a non-negative charge in kobo, rounded upward to a whole
// naira after configured floor/cap values are applied. All arithmetic is integer.
func Calculate(rule Rule, fareKobo int64) (int64, error) {
	if fareKobo < 0 || rule.Base != "trip_fare" {
		return 0, ErrInvalidRule
	}
	var amount int64
	switch rule.Type {
	case "fixed":
		var c struct {
			AmountNaira *int64 `json:"amountNaira"`
		}
		if decode(rule.Config, &c) != nil || c.AmountNaira == nil || *c.AmountNaira < 0 || *c.AmountNaira > math.MaxInt64/100 {
			return 0, ErrInvalidRule
		}
		amount = *c.AmountNaira * 100
	case "percentage":
		var c struct {
			RateBps *int64 `json:"rateBps"`
		}
		if decode(rule.Config, &c) != nil || c.RateBps == nil || *c.RateBps < 0 || *c.RateBps > 1000000 {
			return 0, ErrInvalidRule
		}
		var err error
		amount, err = ceilMulDiv(fareKobo, *c.RateBps, 10000)
		if err != nil {
			return 0, ErrInvalidRule
		}
	case "tiered":
		var c struct {
			Brackets []struct {
				UpToNaira *int64 `json:"upToNaira"`
				RateBps   *int64 `json:"rateBps"`
			} `json:"brackets"`
		}
		if decode(rule.Config, &c) != nil || len(c.Brackets) == 0 || len(c.Brackets) > 32 {
			return 0, ErrInvalidRule
		}
		if c.Brackets[len(c.Brackets)-1].UpToNaira != nil {
			return 0, ErrInvalidRule
		}
		var lower int64
		for i, b := range c.Brackets {
			if b.RateBps == nil || *b.RateBps < 0 || *b.RateBps > 1000000 {
				return 0, ErrInvalidRule
			}
			upper := int64(math.MaxInt64)
			if b.UpToNaira != nil {
				if *b.UpToNaira < 0 || *b.UpToNaira > math.MaxInt64/100 {
					return 0, ErrInvalidRule
				}
				upper = *b.UpToNaira * 100
				if upper <= lower {
					return 0, ErrInvalidRule
				}
			} else if i != len(c.Brackets)-1 {
				return 0, ErrInvalidRule
			}
			portion := fareKobo - lower
			if portion < 0 {
				portion = 0
			}
			if portion > upper-lower {
				portion = upper - lower
			}
			part, err := ceilMulDiv(portion, *b.RateBps, 10000)
			if err != nil || amount > math.MaxInt64-part {
				return 0, ErrInvalidRule
			}
			amount += part
			if b.UpToNaira == nil {
				break
			}
			lower = upper
		}
		if c.Brackets[len(c.Brackets)-1].UpToNaira != nil && fareKobo > lower {
			return 0, ErrInvalidRule
		}
	case "formula":
		var expr any
		if decode(rule.Config, &expr) != nil {
			return 0, ErrInvalidRule
		}
		v, err := eval(expr, fareKobo, 0)
		if err != nil || v < 0 {
			return 0, ErrInvalidRule
		}
		amount = v
	default:
		return 0, ErrInvalidRule
	}
	if rule.Minimum != nil && amount < *rule.Minimum {
		amount = *rule.Minimum
	}
	if rule.Maximum != nil && amount > *rule.Maximum {
		amount = *rule.Maximum
	}
	if amount > math.MaxInt64-99 {
		return 0, ErrInvalidRule
	}
	return ((amount + 99) / 100) * 100, nil
}

func decode(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return ErrInvalidRule
	}
	return nil
}
func ceilMulDiv(a, b, d int64) (int64, error) {
	if d <= 0 {
		return 0, ErrInvalidRule
	}
	n := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	n.Add(n, big.NewInt(d-1))
	n.Quo(n, big.NewInt(d))
	if !n.IsInt64() {
		return 0, ErrInvalidRule
	}
	return n.Int64(), nil
}

// Formula JSON is a bounded expression tree: {"var":"trip_fare_kobo"},
// {"constKobo":123}, or {"op":"add|sub|min|max|mul|div","args":[...]}
// Multiplication/division are limited to two operands and integer kobo results.
func eval(x any, fare int64, depth int) (int64, error) {
	if depth > 12 {
		return 0, ErrInvalidRule
	}
	m, ok := x.(map[string]any)
	if !ok {
		return 0, ErrInvalidRule
	}
	if v, ok := m["var"]; ok {
		if len(m) != 1 || v != "trip_fare_kobo" {
			return 0, ErrInvalidRule
		}
		return fare, nil
	}
	if v, ok := m["constKobo"]; ok {
		if len(m) != 1 {
			return 0, ErrInvalidRule
		}
		n, ok := v.(json.Number)
		if !ok {
			return 0, ErrInvalidRule
		}
		z, ok := new(big.Int).SetString(string(n), 10)
		if !ok || !z.IsInt64() || z.Sign() < 0 {
			return 0, ErrInvalidRule
		}
		return z.Int64(), nil
	}
	if len(m) != 2 {
		return 0, ErrInvalidRule
	}
	op, ok := m["op"].(string)
	if !ok {
		return 0, ErrInvalidRule
	}
	args, ok := m["args"].([]any)
	if !ok || len(args) < 2 || len(args) > 8 {
		return 0, ErrInvalidRule
	}
	vals := make([]int64, len(args))
	for i, a := range args {
		v, e := eval(a, fare, depth+1)
		if e != nil {
			return 0, e
		}
		vals[i] = v
	}
	r := vals[0]
	for _, v := range vals[1:] {
		switch op {
		case "add":
			if r > math.MaxInt64-v {
				return 0, ErrInvalidRule
			}
			r += v
		case "sub":
			if v > r {
				return 0, ErrInvalidRule
			}
			r -= v
		case "min":
			if v < r {
				r = v
			}
		case "max":
			if v > r {
				r = v
			}
		case "mul":
			if len(vals) != 2 {
				return 0, ErrInvalidRule
			}
			z := new(big.Int).Mul(big.NewInt(r), big.NewInt(v))
			if !z.IsInt64() {
				return 0, ErrInvalidRule
			}
			r = z.Int64()
		case "div":
			if len(vals) != 2 || v == 0 {
				return 0, ErrInvalidRule
			}
			n := new(big.Int).Add(big.NewInt(r), big.NewInt(v-1))
			n.Quo(n, big.NewInt(v))
			if !n.IsInt64() {
				return 0, ErrInvalidRule
			}
			r = n.Int64()
		default:
			return 0, fmt.Errorf("%w: unknown formula operation", ErrInvalidRule)
		}
	}
	return r, nil
}
