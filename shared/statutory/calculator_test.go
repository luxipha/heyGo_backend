package statutory

import (
	"encoding/json"
	"testing"
)

func TestCalculateFixedRoundsUpToWholeNaira(t *testing.T) {
	got, err := Calculate(Rule{Type: "fixed", Base: "trip_fare", Config: json.RawMessage(`{"amountNaira":250}`)}, 0)
	if err != nil || got != 25000 {
		t.Fatalf("got %d, %v; want 25000 kobo", got, err)
	}
}
func TestCalculatePercentageRoundsUpToWholeNaira(t *testing.T) {
	got, err := Calculate(Rule{Type: "percentage", Base: "trip_fare", Config: json.RawMessage(`{"rateBps":750}`)}, 33333)
	if err != nil || got != 2500 {
		t.Fatalf("got %d, %v; want N25.00", got, err)
	}
}

func TestChargeOfN250And20KoboRoundsToN251(t *testing.T) {
	got, err := Calculate(Rule{Type: "percentage", Base: "trip_fare", Config: json.RawMessage(`{"rateBps":10000}`)}, 25020)
	if err != nil || got != 25100 {
		t.Fatalf("got %d, %v; want 25100 kobo", got, err)
	}
}
func TestCalculateProgressiveTiers(t *testing.T) {
	cfg := json.RawMessage(`{"brackets":[{"upToNaira":100,"rateBps":1000},{"rateBps":2000}]}`)
	got, err := Calculate(Rule{Type: "tiered", Base: "trip_fare", Config: cfg}, 15000)
	if err != nil || got != 2000 {
		t.Fatalf("got %d, %v; want N20.00", got, err)
	}
}
func TestCalculateRestrictedFormula(t *testing.T) {
	cfg := json.RawMessage(`{"op":"div","args":[{"op":"mul","args":[{"var":"trip_fare_kobo"},{"constKobo":750}]},{"constKobo":10000}]}`)
	got, err := Calculate(Rule{Type: "formula", Base: "trip_fare", Config: cfg}, 33333)
	if err != nil || got != 2500 {
		t.Fatalf("got %d, %v; want N25.00", got, err)
	}
}
func TestRejectsExecutableFormulaAndUnknownFields(t *testing.T) {
	for _, cfg := range []string{`{"op":"exec","args":[{"constKobo":1},{"constKobo":2}]}`, `{"amountNaira":10,"script":"x"}`} {
		if _, err := Calculate(Rule{Type: "formula", Base: "trip_fare", Config: json.RawMessage(cfg)}, 10000); err == nil {
			t.Fatalf("accepted invalid formula %s", cfg)
		}
	}
}
