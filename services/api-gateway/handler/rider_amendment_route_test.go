package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"math"
	"testing"
	"time"
)

type amendmentRouteReader func(context.Context, geo.Coordinate, geo.Coordinate) (*geo.RouteResponse, error)

func (f amendmentRouteReader) Route(ctx context.Context, a, b geo.Coordinate) (*geo.RouteResponse, error) {
	return f(ctx, a, b)
}
func amendmentTestPlace(lat, lng float64) agent.Place {
	raw, _ := json.Marshal(map[string]any{"id": "place", "location": map[string]float64{"latitude": lat, "longitude": lng}})
	var p agent.Place
	json.Unmarshal(raw, &p)
	return p
}
func amendmentTestRouter(t *testing.T, calls *int, provider string) amendmentRouteReader {
	return func(_ context.Context, a, b geo.Coordinate) (*geo.RouteResponse, error) {
		*calls++
		if a == b {
			t.Fatal("zero leg reached provider")
		}
		return &geo.RouteResponse{Provider: provider, Routes: []geo.Route{{Distance: 120, Duration: 30, StaticDuration: 25, TrafficAware: true, Geometry: geo.Geometry{Coordinates: [][]float64{{a.Longitude, a.Latitude}, {b.Longitude, b.Latitude}}}}}}, nil
	}
}
func TestAmendmentRouteAtDestinationUsesZeroBaseline(t *testing.T) {
	ctx := context.Background()
	origin := geo.Coordinate{Latitude: 6.5, Longitude: 3.3}
	calls := 0
	router := amendmentTestRouter(t, &calls, "osrm")
	current, e := amendmentRoute(ctx, router, origin, nil, amendmentTestPlace(6.5, 3.3))
	if e != nil || calls != 0 || current.Routes[0].Distance != 0 || current.Routes[0].Duration != 0 {
		t.Fatal("zero baseline unavailable", e)
	}
	proposed, e := amendmentRoute(ctx, router, origin, nil, amendmentTestPlace(6.6, 3.4))
	if e != nil || calls != 1 {
		t.Fatal("replacement not genuinely routed", e)
	}
	if e = amendmentRouteProviders(current, proposed); e != nil || current.Provider != "osrm" || proposed.Routes[0].Distance-current.Routes[0].Distance != 120 {
		t.Fatal("provider/delta comparison lost", e)
	}
}
func TestAmendmentRouteSkipsOnlyExactlyCoincidentLegs(t *testing.T) {
	origin := geo.Coordinate{Latitude: 6.5, Longitude: 3.3}
	calls := 0
	done := time.Now()
	stops := []amendmentStop{{Place: amendmentTestPlace(6.5, 3.3)}, {Place: amendmentTestPlace(6.6, 3.4)}, {Place: amendmentTestPlace(7, 4), CompletedAt: &done}, {Place: amendmentTestPlace(6.6, 3.4)}}
	route, e := amendmentRoute(context.Background(), amendmentTestRouter(t, &calls, "google"), origin, stops, amendmentTestPlace(6.7, 3.5))
	if e != nil || calls != 2 || route.Routes[0].Distance != 240 || route.Routes[0].Duration != 60 || route.Routes[0].StaticDuration != 50 {
		t.Fatal("nonzero legs/provider estimates changed", e)
	}
	calls = 0
	_, e = amendmentRoute(context.Background(), amendmentTestRouter(t, &calls, "google"), origin, nil, amendmentTestPlace(6.500000001, 3.3))
	if e != nil || calls != 1 {
		t.Fatal("nearby distinct endpoints were treated as zero")
	}
}
func TestAmendmentRouteProviderFailuresRemainClosed(t *testing.T) {
	origin := geo.Coordinate{Latitude: 6.5, Longitude: 3.3}
	router := amendmentRouteReader(func(context.Context, geo.Coordinate, geo.Coordinate) (*geo.RouteResponse, error) {
		return nil, errors.New("provider unavailable")
	})
	if _, e := amendmentRoute(context.Background(), router, origin, nil, amendmentTestPlace(6.6, 3.4)); e == nil {
		t.Fatal("provider failure replaced with fabricated route")
	}
	calls := 0
	mixed := amendmentRouteReader(func(ctx context.Context, a, b geo.Coordinate) (*geo.RouteResponse, error) {
		provider := "osrm"
		if calls > 0 {
			provider = "google"
		}
		return amendmentTestRouter(t, &calls, provider)(ctx, a, b)
	})
	if _, e := amendmentRoute(context.Background(), mixed, origin, []amendmentStop{{Place: amendmentTestPlace(6.6, 3.4)}}, amendmentTestPlace(6.7, 3.5)); e == nil {
		t.Fatal("mixed providers accepted")
	}
	if _, e := amendmentRoute(context.Background(), router, geo.Coordinate{Latitude: math.NaN()}, nil, amendmentTestPlace(6.5, 3.3)); e == nil {
		t.Fatal("invalid origin accepted")
	}
}
func TestAmendmentComparisonRequiresActualProvider(t *testing.T) {
	ctx := context.Background()
	origin := geo.Coordinate{Latitude: 6.5, Longitude: 3.3}
	calls := 0
	router := amendmentTestRouter(t, &calls, "osrm")
	zero, _ := amendmentRoute(ctx, router, origin, nil, amendmentTestPlace(6.5, 3.3))
	otherZero, _ := amendmentRoute(ctx, router, origin, nil, amendmentTestPlace(6.5, 3.3))
	if amendmentRouteProviders(zero, otherZero) == nil {
		t.Fatal("provider identity invented for two zero alternatives")
	}
	osrm, _ := amendmentRoute(ctx, router, origin, nil, amendmentTestPlace(6.6, 3.4))
	google, _ := amendmentRoute(ctx, amendmentTestRouter(t, &calls, "google"), origin, nil, amendmentTestPlace(6.6, 3.4))
	if amendmentRouteProviders(osrm, google) == nil {
		t.Fatal("alternative provider mismatch accepted")
	}
	if e := amendmentRouteProviders(osrm, zero); e != nil || zero.Provider != "osrm" {
		t.Fatal("zero replacement comparison failed", e)
	}
}
