package handler

import "testing"

func TestRiderCategoryImageKeys(t *testing.T) {
	for name, want := range map[string]string{"Go Standard": "go_standard", "Go Comfort": "go_comfort", "Go Green": "go_green", "Go XL": "go_xl", "Go Reserve": "go_reserve", "Go VIP": "go_vip", "Future category": "go_standard"} {
		if got := riderCategoryImageKey(name); got != want {
			t.Fatalf("%s: %s want %s", name, got, want)
		}
	}
}
