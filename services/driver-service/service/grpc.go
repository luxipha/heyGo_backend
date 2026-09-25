package service

import (
	"context"
	"fmt"
	"time"

	"github.com/luxipha/heyGo_backend/services/driver-service/repo"
	pb "github.com/luxipha/heyGo_backend/shared/proto/driver"
	tripproto "github.com/luxipha/heyGo_backend/shared/proto/trip"
)

type driverService struct {
	repo repo.DriverRepo
}

type DriverService interface {
	RegisterDriver(ctx context.Context, driverID, packageSlug string) (*pb.Driver, error)
	UnregisterDriver(ctx context.Context, driverID string) error
	UpdateLocation(ctx context.Context, driverID string, latitude, longitude float64) (string, error)
	MatchAndReserve(ctx context.Context, trip *tripproto.Trip, payload []byte) (*repo.Candidate, error)
	Decline(ctx context.Context, tripID, driverID string) (*repo.RetryTrip, error)
	ExpireOffers(ctx context.Context) ([]repo.RetryTrip, error)
}

func NewDriverService(repo repo.DriverRepo) *driverService {
	return &driverService{repo: repo}
}

func (s *driverService) RegisterDriver(ctx context.Context, driverID, packageSlug string) (*pb.Driver, error) {
	driver, err := s.repo.UpsertOnline(ctx, &pb.Driver{Id: driverID, PackageSlug: packageSlug})
	if err != nil {
		return nil, err
	}
	return driver, nil
}

func (s *driverService) UnregisterDriver(ctx context.Context, driverID string) error {
	return s.repo.SetOffline(ctx, driverID)
}

func (s *driverService) UpdateLocation(ctx context.Context, driverID string, latitude, longitude float64) (string, error) {
	return s.repo.UpdateLocation(ctx, driverID, latitude, longitude)
}

func (s *driverService) MatchAndReserve(ctx context.Context, trip *tripproto.Trip, payload []byte) (*repo.Candidate, error) {
	if trip == nil || trip.SelectedFare == nil || trip.Route == nil || len(trip.Route.Geometry) == 0 || len(trip.Route.Geometry[0].Coordinates) == 0 {
		return nil, fmt.Errorf("trip has no pickup location")
	}
	pickup := trip.Route.Geometry[0].Coordinates[0]
	return s.repo.MatchAndReserve(ctx, trip.Id, trip.SelectedFare.PackageSlug, pickup.Latitude, pickup.Longitude, 15_000, 20*time.Second, payload)
}

func (s *driverService) Decline(ctx context.Context, tripID, driverID string) (*repo.RetryTrip, error) {
	return s.repo.DeclineAssignment(ctx, tripID, driverID)
}

func (s *driverService) ExpireOffers(ctx context.Context) ([]repo.RetryTrip, error) {
	return s.repo.ExpireOffers(ctx, 100)
}
