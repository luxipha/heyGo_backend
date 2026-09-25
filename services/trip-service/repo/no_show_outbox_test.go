package repo

import (
	"encoding/json"
	"testing"
)

func TestWithNoShowDebtAddsDriverFareBreakdown(t *testing.T) {
	data, err := withNoShowDebt([]byte(`{"trip":{"selectedFare":{"id":"fare-1"}}}`), false, 100000, 500000)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Trip struct {
			NoShowDebtKobo int64 `json:"noShowDebtKobo"`
			AmountDueKobo  int64 `json:"amountDueKobo"`
			SelectedFare   struct {
				NoShowDebtKobo int64 `json:"noShowDebtKobo"`
				AmountDueKobo  int64 `json:"amountDueKobo"`
			} `json:"selectedFare"`
		} `json:"trip"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Trip.NoShowDebtKobo != 100000 || envelope.Trip.AmountDueKobo != 600000 || envelope.Trip.SelectedFare.NoShowDebtKobo != 100000 || envelope.Trip.SelectedFare.AmountDueKobo != 600000 {
		t.Fatalf("unexpected no-show fare breakdown: %s", data)
	}
}
