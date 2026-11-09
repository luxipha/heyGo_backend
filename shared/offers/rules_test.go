package offers

import (
	"testing"
	"time"
)

func TestOfferRulesAndLagosWindow(t *testing.T) {
	now := time.Date(2026, 11, 7, 5, 0, 0, 0, time.UTC) // 06:00 Lagos.
	r := Rule{Kind: "discount", Discount: 50000, Multiplier: 1, Budget: 100000, Starts: now.Add(-time.Hour), Ends: now.Add(24 * time.Hour), Markets: []string{"LAGOS"}, Categories: []string{"go"}, Timezone: "Africa/Lagos", StartHour: 6, EndHour: 9, MaxUses: 2, PerRider: 1, Active: true}
	if err := Validate(r); err != nil {
		t.Fatal(err)
	}
	if !WithinWindow(r, now) || WithinWindow(r, now.Add(3*time.Hour)) || WithinWindow(r, now.Add(-time.Second)) {
		t.Fatal("window boundaries")
	}
	r.Budget = 100
	if Validate(r) == nil {
		t.Fatal("unfunded discount accepted")
	}
	r.Kind = "gocoin_multiplier"
	r.Discount = 0
	r.Multiplier = 2
	if err := Validate(r); err != nil {
		t.Fatal(err)
	}
	r.Multiplier = 11
	if Validate(r) == nil {
		t.Fatal("unbounded multiplier accepted")
	}
	if NormalizeCode(" welcome500 ") != "WELCOME500" || ValidCode("bad code") || !ValidCode("WELCOME500") {
		t.Fatal("promo-code validation")
	}
}
