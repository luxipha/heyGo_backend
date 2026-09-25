package repo

import (
	"encoding/json"
	"testing"

	tripproto "github.com/cprakhar/uber-clone/shared/proto/trip"
)

func TestMergeNoShowDebtOfferFields(t *testing.T) {
	source := []byte(`{"trip":{"id":"trip-1","noShowDebtKobo":100000,"amountDueKobo":600000,"selectedFare":{"id":"fare-1"}}}`)
	trip, err := json.Marshal(&tripproto.Trip{Id: "trip-1", SelectedFare: &tripproto.RideFare{Id: "fare-1", TotalFareInPaise: 500000}})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := mergeNoShowDebtOfferFields(source, trip)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatal(err)
	}
	var debt, due int64
	if err := json.Unmarshal(got["noShowDebtKobo"], &debt); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got["amountDueKobo"], &due); err != nil {
		t.Fatal(err)
	}
	if debt != 100000 || due != 600000 {
		t.Fatalf("trip debt=%d due=%d", debt, due)
	}
	var fare map[string]json.RawMessage
	if err := json.Unmarshal(got["selectedFare"], &fare); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fare["noShowDebtKobo"], &debt); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fare["amountDueKobo"], &due); err != nil {
		t.Fatal(err)
	}
	if debt != 100000 || due != 600000 {
		t.Fatalf("selected fare debt=%d due=%d", debt, due)
	}
}
