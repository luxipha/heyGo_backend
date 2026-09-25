package service

import (
	"testing"

	triptypes "github.com/luxipha/heyGo_backend/services/trip-service/types"
)

func TestEstimateFareRoute(t *testing.T) {
	route := &triptypes.OSRMApiResponse{}
	route.Routes = append(route.Routes, struct {
		Distance float64 `json:"distance"`
		Duration float64 `json:"duration"`
		Geometry struct {
			Coordinates [][]float64 `json:"coordinates"`
		} `json:"geometry"`
	}{Distance: 12.5, Duration: 8})

	got := estimateFareRoute(route, &triptypes.RideFareModel{PackageSlug: "sedan", TotalFareInPaise: 100})
	if got == nil {
		t.Fatal("expected an estimated fare")
	}
	want := 100.0 + 12.5*10.0 + 8.0*5.0
	if got.TotalFareInPaise != want || got.PackageSlug != "sedan" {
		t.Fatalf("unexpected fare: got %#v, want total %v", got, want)
	}
}

func TestEstimateFareRouteRejectsMissingInputs(t *testing.T) {
	if got := estimateFareRoute(nil, &triptypes.RideFareModel{}); got != nil {
		t.Fatalf("expected nil fare, got %#v", got)
	}
	if got := estimateFareRoute(&triptypes.OSRMApiResponse{}, nil); got != nil {
		t.Fatalf("expected nil fare, got %#v", got)
	}
}
