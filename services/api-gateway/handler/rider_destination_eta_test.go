package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"testing"
	"time"
)

type destinationETARow struct{ unchanged bool }

func (r destinationETARow) Scan(dest ...any) error { *(dest[0].(*bool)) = r.unchanged; return nil }

type destinationETADB struct {
	unchanged bool
	calls     int
}

func (d *destinationETADB) QueryRow(context.Context, string, ...any) pgx.Row {
	d.calls++
	return destinationETARow{d.unchanged}
}

type destinationETARouter struct {
	points [][2]geo.Coordinate
	fail   bool
}

func (r *destinationETARouter) Route(ctx context.Context, a, b geo.Coordinate) (*geo.RouteResponse, error) {
	r.points = append(r.points, [2]geo.Coordinate{a, b})
	if r.fail {
		return nil, errors.New("provider unavailable")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return &geo.RouteResponse{Provider: "road-test", Routes: []geo.Route{{Duration: 120.4, Distance: 1000, TrafficAware: true, Geometry: geo.Geometry{Coordinates: [][]float64{{a.Longitude, a.Latitude}, {b.Longitude, b.Latitude}}}}}}, nil
}
func destinationETASnapshot(at time.Time) riderTripSnapshot {
	driver, _ := json.Marshal(map[string]any{"id": "driver", "location": map[string]any{"latitude": 6.4, "longitude": 3.4, "recordedAt": at}})
	return riderTripSnapshot{ID: "trip", Status: "started", Version: 7, RouteRevision: 3, Driver: driver, Destination: json.RawMessage(`{"latitude":6.8,"longitude":3.8}`), Stops: json.RawMessage(`[]`)}
}

func TestRiderDestinationETARemainingStopsAndFreshness(t *testing.T) {
	recorded := time.Now().UTC().Add(-110 * time.Second)
	trip := destinationETASnapshot(recorded)
	trip.Stops = json.RawMessage(`[{"id":"done","completedAt":"2026-01-01T00:00:00Z","place":{"location":{"latitude":6.5,"longitude":3.5}}},{"id":"next","place":{"location":{"latitude":6.6,"longitude":3.6}}}]`)
	router := &destinationETARouter{}
	db := &destinationETADB{unchanged: true}
	enrichDestinationETA(context.Background(), db, router, &trip)
	eta := trip.DestinationETA
	if eta.Status != "estimated" || eta.RemainingDurationSeconds == nil || *eta.RemainingDurationSeconds != 241 || len(router.points) != 2 {
		t.Fatalf("remaining route: %+v legs=%d", eta, len(router.points))
	}
	if router.points[0][0].Latitude != 6.4 || router.points[0][1].Latitude != 6.6 || router.points[1][1].Latitude != 6.8 {
		t.Fatal("wrong origin/stops/destination", router.points)
	}
	if eta.EstimatedArrivalAt == nil || !eta.EstimatedArrivalAt.Equal(eta.EstimatedAt.Add(241*time.Second)) || eta.ExpiresAt == nil || !eta.ExpiresAt.Equal(recorded.Add(2*time.Minute)) {
		t.Fatalf("timestamps: %+v", eta)
	}
	if eta.TripVersion != 7 || eta.RouteRevision != 3 || eta.Provider != "road-test" || !eta.TrafficAware || eta.IncludesStopWaitingTime || db.calls != 1 {
		t.Fatalf("metadata: %+v", eta)
	}
}

func TestRiderDestinationETAUnavailableAndNonTripStates(t *testing.T) {
	for _, status := range []string{"pending", "assigned", "accepted", "arrived", "completed", "cancelled"} {
		trip := destinationETASnapshot(time.Now())
		trip.Status = status
		router := &destinationETARouter{}
		enrichDestinationETA(context.Background(), &destinationETADB{unchanged: true}, router, &trip)
		if trip.DestinationETA.Status != "not_applicable" || len(router.points) != 0 {
			t.Fatal(status, trip.DestinationETA)
		}
	}
	for _, tc := range []struct {
		name          string
		age           time.Duration
		mutate        func(*riderTripSnapshot)
		fail, changed bool
	}{
		{name: "stale GPS", age: 3 * time.Minute},
		{name: "future GPS", age: -time.Minute},
		{name: "missing location", mutate: func(t *riderTripSnapshot) { t.Driver = json.RawMessage(`{"id":"driver"}`) }},
		{name: "bad destination", mutate: func(t *riderTripSnapshot) { t.Destination = json.RawMessage(`null`) }},
		{name: "bad stops", mutate: func(t *riderTripSnapshot) { t.Stops = json.RawMessage(`{}`) }},
		{name: "provider failure", fail: true},
		{name: "trip changed during routing", changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trip := destinationETASnapshot(time.Now().Add(-tc.age))
			if tc.mutate != nil {
				tc.mutate(&trip)
			}
			enrichDestinationETA(context.Background(), &destinationETADB{unchanged: !tc.changed}, &destinationETARouter{fail: tc.fail}, &trip)
			if trip.DestinationETA.Status != "unavailable" || trip.DestinationETA.EstimatedArrivalAt != nil || trip.DestinationETA.RemainingDurationSeconds != nil {
				t.Fatalf("fabricated estimate: %+v", trip.DestinationETA)
			}
		})
	}
}
