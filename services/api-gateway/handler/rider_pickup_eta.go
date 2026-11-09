package handler

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/driverstate"
	"github.com/luxipha/heyGo_backend/shared/geo"
)

type pickupETAStore interface {
	riderHomeDB
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func previewPickupETA(ctx context.Context, pool pickupETAStore, router geo.RouteService, fareID, owner string) geo.PickupEstimate {
	unavailable := geo.PickupEstimate{Status: "unavailable", ObservedAt: time.Now().UTC()}
	var pickup geo.Coordinate
	var market, slug string
	err := pool.QueryRow(ctx, `SELECT ST_Y(pickup::geometry),ST_X(pickup::geometry),pricing_market_code,package_slug FROM ride_fares WHERE id=$1::uuid AND rider_id=$2::uuid`, fareID, owner).Scan(&pickup.Latitude, &pickup.Longitude, &market, &slug)
	if err != nil {
		return unavailable
	}
	rows, err := pool.Query(ctx, `SELECT ST_Y(l.location::geometry),ST_X(l.location::geometry),l.recorded_at FROM drivers d JOIN driver_eligibility e ON e.driver_id=d.id JOIN driver_live_locations l ON l.driver_id=d.id WHERE `+driverstate.AvailabilityPredicate("$1", "$2", "ST_SetSRID(ST_MakePoint($4,$3),4326)::geography", "$5")+` AND l.recorded_at>=NOW()-INTERVAL '2 minutes' AND driver_market_at(l.location)=$1 AND ST_DWithin(l.location,ST_SetSRID(ST_MakePoint($4,$3),4326)::geography,$5) ORDER BY l.location <-> ST_SetSRID(ST_MakePoint($4,$3),4326)::geography,d.id LIMIT 3`, market, slug, pickup.Latitude, pickup.Longitude, driverstate.MatchingRadiusMeters)
	if err != nil {
		return unavailable
	}
	candidates := []geo.PickupCandidate{}
	for rows.Next() {
		var c geo.PickupCandidate
		if rows.Scan(&c.Location.Latitude, &c.Location.Longitude, &c.RecordedAt) != nil {
			rows.Close()
			return unavailable
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return unavailable
	}
	return geo.EstimatePickup(ctx, router, candidates, pickup, time.Now())
}

// Shared only for this preview request: categories often use the same drivers.
type previewRouteCache struct {
	router geo.RouteService
	routes map[[4]float64]*geo.RouteResponse
}

func (r *previewRouteCache) Route(ctx context.Context, a, b geo.Coordinate) (*geo.RouteResponse, error) {
	key := [4]float64{a.Latitude, a.Longitude, b.Latitude, b.Longitude}
	if route, ok := r.routes[key]; ok {
		return route, nil
	}
	route, err := r.router.Route(ctx, a, b)
	if err == nil {
		r.routes[key] = route
	}
	return route, err
}

func enrichAssignedPickupETA(ctx context.Context, pool riderHomeDB, router geo.RouteService, trip *riderTripSnapshot) {
	if trip.Status != "accepted" && trip.Status != "arrived" {
		return
	}
	var assignment map[string]any
	if json.Unmarshal(trip.Assignment, &assignment) != nil || assignment == nil {
		return
	}
	result := geo.PickupEstimate{Status: "unavailable", ObservedAt: time.Now().UTC()}
	if trip.Status == "arrived" {
		zero := int64(0)
		result.Status = "arrived"
		result.Seconds = &zero
	} else {
		var c geo.PickupCandidate
		var pickup geo.Coordinate
		err := pool.QueryRow(ctx, `SELECT ST_Y(l.location::geometry),ST_X(l.location::geometry),l.recorded_at,ST_Y(f.pickup::geometry),ST_X(f.pickup::geometry) FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id JOIN driver_live_locations l ON l.driver_id=t.assigned_driver_id WHERE t.id=$1::uuid`, trip.ID).Scan(&c.Location.Latitude, &c.Location.Longitude, &c.RecordedAt, &pickup.Latitude, &pickup.Longitude)
		if err == nil {
			result = geo.EstimatePickup(ctx, router, []geo.PickupCandidate{c}, pickup, time.Now())
		}
	}
	assignment["pickupEtaSeconds"] = result.Seconds
	assignment["pickupEtaStatus"] = result.Status
	assignment["pickupEtaObservedAt"] = result.ObservedAt
	if result.Provider != "" {
		assignment["pickupEtaProvider"] = result.Provider
	}
	if raw, err := json.Marshal(assignment); err == nil {
		trip.Assignment = raw
	}
}
