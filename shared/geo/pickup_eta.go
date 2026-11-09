package geo

import (
	"context"
	"math"
	"time"
)

// PickupEstimate is an estimate, never a dispatch or arrival guarantee.
type PickupEstimate struct {
	Status     string    `json:"status"`
	Seconds    *int64    `json:"pickupEtaSeconds"`
	ObservedAt time.Time `json:"observedAt"`
	Provider   string    `json:"provider,omitempty"`
}

type PickupCandidate struct {
	Location   Coordinate
	RecordedAt time.Time
}

// EstimatePickup uses bounded road-route lookups, not straight-line travel time.
// Callers must first apply dispatch eligibility to preview candidates.
func EstimatePickup(ctx context.Context, router RouteService, candidates []PickupCandidate, pickup Coordinate, now time.Time) PickupEstimate {
	result := PickupEstimate{Status: "unavailable", ObservedAt: now.UTC()}
	if !pickup.Valid() || router == nil {
		return result
	}
	fresh := 0
	for _, candidate := range candidates {
		age := now.Sub(candidate.RecordedAt)
		if !candidate.Location.Valid() || age < -5*time.Second || age > 2*time.Minute {
			continue
		}
		fresh++
		if fresh > 3 {
			break
		}
		routeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		response, err := router.Route(routeCtx, candidate.Location, pickup)
		cancel()
		if err != nil || response == nil || len(response.Routes) == 0 {
			continue
		}
		duration := response.Routes[0].Duration
		if math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 0 || duration > 86400 {
			continue
		}
		seconds := int64(math.Ceil(duration))
		if result.Seconds == nil || seconds < *result.Seconds {
			result.Seconds = &seconds
			result.Provider = response.Provider
			result.Status = "estimated"
		}
	}
	if fresh == 0 {
		result.Status = "no_fresh_drivers"
	}
	return result
}
