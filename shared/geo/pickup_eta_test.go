package geo

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

type pickupTestRouter struct {
	calls    int
	duration float64
	err      error
}

func (r *pickupTestRouter) Route(ctx context.Context, a, b Coordinate) (*RouteResponse, error) {
	r.calls++
	return &RouteResponse{Provider: "test", Routes: []Route{{Duration: r.duration + a.Latitude}}}, r.err
}
func TestPickupETAUsesFreshRoadRoutesAndBoundsWork(t *testing.T) {
	now := time.Now()
	r := &pickupTestRouter{duration: 100.2}
	candidates := []PickupCandidate{{Location: Coordinate{Latitude: 1}, RecordedAt: now.Add(-3 * time.Minute)}, {Location: Coordinate{Latitude: 5}, RecordedAt: now}, {Location: Coordinate{Latitude: 2}, RecordedAt: now}, {Location: Coordinate{Latitude: 8}, RecordedAt: now}, {Location: Coordinate{Latitude: 0}, RecordedAt: now}}
	got := EstimatePickup(context.Background(), r, candidates, Coordinate{}, now)
	if got.Seconds == nil || *got.Seconds != 103 || got.Status != "estimated" || r.calls != 3 {
		t.Fatalf("estimate=%+v calls=%d", got, r.calls)
	}
}
func TestPickupETADoesNotInventTime(t *testing.T) {
	now := time.Now()
	fresh := []PickupCandidate{{Location: Coordinate{}, RecordedAt: now}}
	for _, r := range []*pickupTestRouter{{err: errors.New("routing failed")}, {duration: math.NaN()}, {duration: -1}, {duration: math.Inf(1)}} {
		got := EstimatePickup(context.Background(), r, fresh, Coordinate{}, now)
		if got.Seconds != nil || got.Status != "unavailable" {
			t.Fatalf("fabricated ETA: %+v", got)
		}
	}
	r := &pickupTestRouter{duration: 100}
	got := EstimatePickup(context.Background(), r, []PickupCandidate{{RecordedAt: now.Add(-3 * time.Minute)}}, Coordinate{}, now)
	if got.Status != "no_fresh_drivers" || r.calls != 0 {
		t.Fatalf("stale driver used: %+v", got)
	}
}
