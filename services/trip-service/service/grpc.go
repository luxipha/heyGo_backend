package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/services/trip-service/repo"
	"github.com/luxipha/heyGo_backend/services/trip-service/types"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/proto/trip"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
)

var routeHTTPClient = &http.Client{Timeout: 10 * time.Second}

type tripService struct {
	repo repo.TripRepo
}

type TripService interface {
	CreateTrip(ctx context.Context, fare *types.RideFareModel) (*types.TripModel, error)
	GetRoute(ctx context.Context, pickup, destination *sharedtypes.Coordinate) (*types.OSRMApiResponse, error)
	GenerateTripFares(ctx context.Context, riderID string, route *types.OSRMApiResponse, pickup, destination *sharedtypes.Coordinate) ([]*types.RideFareModel, error)
	GetAndValidateRideFare(ctx context.Context, fareID, riderID string) (*types.RideFareModel, error)
	AcceptRide(ctx context.Context, tripID string, driver *trip.TripDriver) (*types.TripModel, error)
	GetTripByID(ctx context.Context, tripID string) (*types.TripModel, error)
	ArriveAtPickup(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error)
	StartTrip(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error)
	CompleteTrip(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error)
	CancelTrip(ctx context.Context, tripID, actorID, reason string) (*types.TripModel, bool, error)
	TrustParticipants(ctx context.Context, tripID string) ([]repo.TrustSubject, error)
	RateTrip(ctx context.Context, tripID, actorID string, rating int, feedbackTags []string, comment string) (repo.TrustSubject, bool, error)
}

// NewService creates a new instance of GrpcTripService
func NewService(repo repo.TripRepo) *tripService {
	return &tripService{repo: repo}
}

func (s *tripService) GetTripByID(ctx context.Context, tripID string) (*types.TripModel, error) {
	return s.repo.GetByID(ctx, tripID)
}

// CreateTrip creates a new trip based on the provided fare
func (s *tripService) CreateTrip(ctx context.Context, fare *types.RideFareModel) (*types.TripModel, error) {
	trip := &types.TripModel{
		ID:       uuid.NewString(),
		RiderID:  fare.RiderID,
		Status:   "pending",
		RideFare: fare,
		Driver:   &trip.TripDriver{},
	}
	return s.repo.Create(ctx, trip)
}

// GetRoute fetches the route from OSRM API between pickup and destination coordinates
func (s *tripService) GetRoute(ctx context.Context, pickup, destination *sharedtypes.Coordinate) (*types.OSRMApiResponse, error) {
	url := fmt.Sprintf("%s/%f,%f;%f,%f?overview=full&geometries=geojson",
		strings.TrimSuffix(env.GetString("OSRM_API", "http://router.project-osrm.org/route/v1/driving"), "/"),
		pickup.Longitude, pickup.Latitude,
		destination.Longitude, destination.Latitude,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build route request: %w", err)
	}
	res, err := routeHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("route service returned %s", res.Status)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var routeResponse types.OSRMApiResponse
	if err := json.Unmarshal(body, &routeResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	if len(routeResponse.Routes) == 0 || len(routeResponse.Routes[0].Geometry.Coordinates) == 0 {
		return nil, fmt.Errorf("route service returned no usable route")
	}

	return &routeResponse, nil
}

// GenerateTripFares uses only published pickup-market rates and freezes the
// selected pricing version on each short-lived fare quote.
func (s *tripService) GenerateTripFares(ctx context.Context, riderID string, route *types.OSRMApiResponse, pickup, destination *sharedtypes.Coordinate) ([]*types.RideFareModel, error) {
	if pickup == nil || route == nil || len(route.Routes) == 0 {
		return nil, repo.ErrPricingUnavailable
	}
	pricing, err := s.repo.PricingForQuote(ctx, pickup.Latitude, pickup.Longitude)
	if err != nil {
		return nil, err
	}
	fares := make([]*types.RideFareModel, len(pricing.Rates))
	for i, rate := range pricing.Rates {
		amount, valid := calculateFareKobo(route.Routes[0].Distance, route.Routes[0].Duration, rate)
		if !valid {
			return nil, fmt.Errorf("fare outside approved bounds")
		}
		f := &types.RideFareModel{
			RiderID:           riderID,
			ID:                uuid.NewString(),
			PackageSlug:       rate.PackageSlug,
			TotalFareInPaise:  amount,
			PricingVersionID:  pricing.ID,
			PricingMarketCode: pricing.MarketCode,
			Route:             route,
			Pickup:            pickup,
			Destination:       destination,
		}

		fares[i] = f
	}
	for _, fare := range fares {
		if err := s.repo.SaveRideFare(ctx, fare); err != nil {
			return nil, err
		}
	}

	return fares, nil
}

// GetAndValidateRideFare retrieves a ride fare by ID and validates that it belongs to the specified rider
func (s *tripService) GetAndValidateRideFare(ctx context.Context, fareID, riderID string) (*types.RideFareModel, error) {
	fare, err := s.repo.GetRideFareByID(ctx, fareID)
	if err != nil {
		return nil, err
	}

	if fare == nil {
		return nil, fmt.Errorf("ride fare not found")
	}

	if fare.RiderID != riderID {
		return nil, fmt.Errorf("ride fare does not belong to rider")
	}

	return fare, nil
}

// AcceptRide allows a driver to accept a trip, updating the trip with the driver's details
func (s *tripService) AcceptRide(ctx context.Context, tripID string, driver *trip.TripDriver) (*types.TripModel, error) {
	return s.repo.UpdateWithDriver(ctx, tripID, driver)
}

func (s *tripService) StartTrip(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error) {
	return s.repo.Start(ctx, tripID, actorID)
}

func (s *tripService) ArriveAtPickup(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error) {
	return s.repo.Arrive(ctx, tripID, actorID)
}

func (s *tripService) CompleteTrip(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error) {
	return s.repo.Complete(ctx, tripID, actorID)
}
func (s *tripService) CancelTrip(ctx context.Context, tripID, actorID, reason string) (*types.TripModel, bool, error) {
	return s.repo.Cancel(ctx, tripID, actorID, reason)
}
func (s *tripService) TrustParticipants(ctx context.Context, tripID string) ([]repo.TrustSubject, error) {
	return s.repo.TrustParticipants(ctx, tripID)
}
func (s *tripService) RateTrip(ctx context.Context, tripID, actorID string, rating int, feedbackTags []string, comment string) (repo.TrustSubject, bool, error) {
	return s.repo.Rate(ctx, tripID, actorID, rating, feedbackTags, comment)
}

func calculateFareKobo(distanceMeters, durationSeconds float64, rate repo.PackageRate) (float64, bool) {
	if distanceMeters < 0 || durationSeconds < 0 || math.IsNaN(distanceMeters) || math.IsNaN(durationSeconds) || math.IsInf(distanceMeters, 0) || math.IsInf(durationSeconds, 0) {
		return 0, false
	}
	amount := math.Round(float64(rate.BaseKobo) + distanceMeters*float64(rate.PerKmKobo)/1000 + durationSeconds*float64(rate.PerMinuteKobo)/60)
	return amount, amount > 0 && amount <= 10000000 && !math.IsInf(amount, 0)
}
