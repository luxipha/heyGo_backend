package service

import (
	"context"
	"testing"
	"time"

	"github.com/cprakhar/uber-clone/services/driver-service/repo"
	driverpb "github.com/cprakhar/uber-clone/shared/proto/driver"
	trippb "github.com/cprakhar/uber-clone/shared/proto/trip"
)

type matchingRepo struct {
	tripID, packageSlug string
	lat, lng, radius    float64
	ttl                 time.Duration
	payload             []byte
}

func (r *matchingRepo) UpsertOnline(context.Context, *driverpb.Driver) (*driverpb.Driver, error) {
	return nil, nil
}
func (r *matchingRepo) SetOffline(context.Context, string) error { return nil }
func (r *matchingRepo) UpdateLocation(context.Context, string, float64, float64) (string, error) {
	return "", nil
}
func (r *matchingRepo) MatchAndReserve(_ context.Context, tripID, packageSlug string, lat, lng, radius float64, ttl time.Duration, payload []byte) (*repo.Candidate, error) {
	r.tripID, r.packageSlug, r.lat, r.lng, r.radius, r.ttl, r.payload = tripID, packageSlug, lat, lng, radius, ttl, payload
	return &repo.Candidate{Driver: &driverpb.Driver{Id: "driver-1"}}, nil
}
func (r *matchingRepo) DeclineAssignment(context.Context, string, string) (*repo.RetryTrip, error) {
	return nil, nil
}
func (r *matchingRepo) ExpireOffers(context.Context, int) ([]repo.RetryTrip, error) {
	return nil, nil
}

func TestMatchAndReserveUsesPickupAndMatchingPolicy(t *testing.T) {
	repository := &matchingRepo{}
	svc := NewDriverService(repository)
	payload := []byte(`{"trip":{"id":"trip-1"}}`)
	trip := &trippb.Trip{
		Id:           "trip-1",
		SelectedFare: &trippb.RideFare{PackageSlug: "sedan"},
		Route:        &trippb.Route{Geometry: []*trippb.Geometry{{Coordinates: []*trippb.Coordinate{{Latitude: 6.45, Longitude: 3.39}}}}},
	}

	candidate, err := svc.MatchAndReserve(context.Background(), trip, payload)
	if err != nil {
		t.Fatalf("match driver: %v", err)
	}
	if candidate.Driver.Id != "driver-1" || repository.tripID != "trip-1" || repository.packageSlug != "sedan" {
		t.Fatalf("unexpected matching call: candidate=%#v repo=%#v", candidate, repository)
	}
	if repository.lat != 6.45 || repository.lng != 3.39 || repository.radius != 15_000 || repository.ttl != 20*time.Second {
		t.Fatalf("unexpected matching policy: %#v", repository)
	}
}

func TestMatchAndReserveRejectsTripWithoutPickup(t *testing.T) {
	_, err := NewDriverService(&matchingRepo{}).MatchAndReserve(context.Background(), &trippb.Trip{}, nil)
	if err == nil {
		t.Fatal("expected missing pickup to be rejected")
	}
}
