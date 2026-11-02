package zonepricing

import (
	"math"
	"testing"
)

func TestApplyBoundsAndRounding(t *testing.T) {
	cases := []struct {
		amount float64
		r      *Rule
		want   float64
		ok     bool
	}{
		{1001, &Rule{ZoneID: "zone", Kind: "multiplier", Value: 12500}, 1251, true},
		{1000, &Rule{ZoneID: "zone", Kind: "fixed", Value: 200}, 1200, true},
		{1000, nil, 1000, true}, {0, nil, 0, false}, {math.NaN(), nil, 0, false},
		{9000000, &Rule{ZoneID: "zone", Kind: "multiplier", Value: 20000}, 18000000, false},
		{100, &Rule{ZoneID: "zone", Kind: "fixed", Value: -1}, 0, false},
	}
	for _, c := range cases {
		got, ok := Apply(c.amount, c.r)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("got %v %v want %v %v", got, ok, c.want, c.ok)
		}
	}
}
