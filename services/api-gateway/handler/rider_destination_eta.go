package handler

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	"github.com/luxipha/heyGo_backend/shared/geo"
)

type riderDestinationETA struct {
	Status                   string     `json:"status"`
	RemainingDurationSeconds *int64     `json:"remainingDurationSeconds"`
	EstimatedArrivalAt       *time.Time `json:"estimatedArrivalAt"`
	EstimatedAt              time.Time  `json:"estimatedAt"`
	ExpiresAt                *time.Time `json:"expiresAt"`
	LocationRecordedAt       *time.Time `json:"locationRecordedAt"`
	TripVersion              int64      `json:"tripVersion"`
	RouteRevision            int64      `json:"routeRevision"`
	Provider                 string     `json:"provider,omitempty"`
	TrafficAware             bool       `json:"trafficAware"`
	IncludesStopWaitingTime  bool       `json:"includesStopWaitingTime"`
}

// Estimates are recomputed on snapshot reads, never derived from the booking
// quote or remaining straight-line distance. Routing failure must not break
// trip recovery. The shared route abstraction chooses the configured provider.
func enrichDestinationETA(ctx context.Context, db riderHomeDB, router geo.RouteService, trip *riderTripSnapshot) {
	now := time.Now().UTC()
	result := riderDestinationETA{Status: "not_applicable", EstimatedAt: now, TripVersion: trip.Version, RouteRevision: trip.RouteRevision}
	trip.DestinationETA = result
	if trip.Status != "started" {
		return
	}
	trip.DestinationETA.Status = "unavailable"
	if router == nil || db == nil {
		return
	}
	var driver struct {
		ID       string `json:"id"`
		Location *struct {
			Latitude   float64   `json:"latitude"`
			Longitude  float64   `json:"longitude"`
			RecordedAt time.Time `json:"recordedAt"`
		} `json:"location"`
	}
	var destination *geo.Coordinate
	var stops []amendmentStop
	if json.Unmarshal(trip.Driver, &driver) != nil || driver.ID == "" || driver.Location == nil || json.Unmarshal(trip.Destination, &destination) != nil || destination == nil || !destination.Valid() || json.Unmarshal(trip.Stops, &stops) != nil || len(stops) > 3 {
		return
	}
	location := driver.Location
	origin := geo.Coordinate{Latitude: location.Latitude, Longitude: location.Longitude}
	age := now.Sub(location.RecordedAt)
	if !origin.Valid() || location.RecordedAt.IsZero() || age > 2*time.Minute || age < -5*time.Second {
		return
	}
	// The estimate expires within 30 seconds, and never outlives GPS freshness.
	expires := now.Add(30 * time.Second)
	if location.RecordedAt.Add(2 * time.Minute).Before(expires) {
		expires = location.RecordedAt.Add(2 * time.Minute)
	}
	routeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := amendmentRoute(routeCtx, router, origin, stops, agent.Place{Location: agent.Coordinate{Latitude: destination.Latitude, Longitude: destination.Longitude}})
	if err != nil || routeCtx.Err() != nil || !expires.After(time.Now()) {
		return
	}
	seconds := response.Routes[0].Duration
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > 86400 {
		return
	}
	// An amendment, stop completion or trip end during routing invalidates this
	// result. Never publish a quote for an obsolete trip version or assignment.
	var unchanged bool
	err = db.QueryRow(routeCtx, `SELECT EXISTS(SELECT 1 FROM trips WHERE id=$1::uuid AND status='started' AND version=$2 AND route_revision=$3 AND assigned_driver_id=$4::uuid)`, trip.ID, trip.Version, trip.RouteRevision, driver.ID).Scan(&unchanged)
	if err != nil || !unchanged || routeCtx.Err() != nil || !expires.After(time.Now()) {
		return
	}
	duration := int64(math.Ceil(seconds))
	arrival := now.Add(time.Duration(duration) * time.Second)
	result.Status = "estimated"
	result.RemainingDurationSeconds = &duration
	result.EstimatedArrivalAt = &arrival
	result.ExpiresAt = &expires
	result.LocationRecordedAt = &location.RecordedAt
	result.Provider = response.Provider
	result.TrafficAware = response.Routes[0].TrafficAware
	trip.DestinationETA = result
}
