package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luxipha/heyGo_backend/services/trip-service/repo"
	"github.com/luxipha/heyGo_backend/services/trip-service/types"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/proto/trip"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
	"github.com/google/uuid"
)

var routeHTTPClient = &http.Client{Timeout: 10 * time.Second}

type tripService struct {
	repo repo.TripRepo
}

type TripService interface {
	CreateTrip(ctx context.Context, fare *types.RideFareModel) (*types.TripModel, error)
	GetRoute(ctx context.Context, pickup, destination *sharedtypes.Coordinate) (*types.OSRMApiResponse, error)
	EstimatePackagesPriceWithRoute(route *types.OSRMApiResponse) []*types.RideFareModel
	GenerateTripFares(ctx context.Context, fares []*types.RideFareModel, riderID string, route *types.OSRMApiResponse, pickup, destination *sharedtypes.Coordinate) ([]*types.RideFareModel, error)
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

// EstimatePackagesPriceWithRoute estimates prices for different car packages based on the provided route
func (s *tripService) EstimatePackagesPriceWithRoute(route *types.OSRMApiResponse) []*types.RideFareModel {
	baseFares := getBaseFares()
	estimatedFares := make([]*types.RideFareModel, len(baseFares))

	for i, fare := range baseFares {
		estimatedFares[i] = estimateFareRoute(route, fare)
	}
	return estimatedFares
}

// GenerateTripFares generates and saves ride fares for a rider based on the provided estimated fares and route
func (s *tripService) GenerateTripFares(ctx context.Context, rideFares []*types.RideFareModel, riderID string, route *types.OSRMApiResponse, pickup, destination *sharedtypes.Coordinate) ([]*types.RideFareModel, error) {
	fares := make([]*types.RideFareModel, len(rideFares))
	for i, fare := range rideFares {
		f := &types.RideFareModel{
			RiderID:          riderID,
			ID:               uuid.NewString(),
			PackageSlug:      fare.PackageSlug,
			TotalFareInPaise: fare.TotalFareInPaise,
			Route:            route,
			Pickup:           pickup,
			Destination:      destination,
		}

		if err := s.repo.SaveRideFare(ctx, f); err != nil {
			return nil, err
		}
		fares[i] = f
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

// estimateFareRoute estimates the total fare for a given route and base fare
func estimateFareRoute(route *types.OSRMApiResponse, fare *types.RideFareModel) *types.RideFareModel {
	if route == nil || len(route.Routes) == 0 || fare == nil {
		return nil
	}
	pricingCfg := types.DefaultPricingConfig()
	carPackagePrice := fare.TotalFareInPaise

	distanceInKm := route.Routes[0].Distance
	durationInMinutes := route.Routes[0].Duration

	distanceFare := distanceInKm * pricingCfg.PricePerUnitDistance
	durationFare := durationInMinutes * pricingCfg.PricePerMinute

	totalFare := carPackagePrice + distanceFare + durationFare

	return &types.RideFareModel{
		PackageSlug:      fare.PackageSlug,
		TotalFareInPaise: totalFare,
	}
}

// getBaseFares returns a list of base fares for different car packages
func getBaseFares() []*types.RideFareModel {
	return []*types.RideFareModel{
		{
			PackageSlug:      "bike",
			TotalFareInPaise: 50.0,
		},
		{
			PackageSlug:      "auto",
			TotalFareInPaise: 70.0,
		},
		{
			PackageSlug:      "sedan",
			TotalFareInPaise: 100.0,
		},
		{
			PackageSlug:      "suv",
			TotalFareInPaise: 150.0,
		},
	}
}
