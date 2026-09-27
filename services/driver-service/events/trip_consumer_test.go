package events

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/luxipha/heyGo_backend/services/driver-service/repo"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	driverpb "github.com/luxipha/heyGo_backend/shared/proto/driver"
	trippb "github.com/luxipha/heyGo_backend/shared/proto/trip"
)

func TestMain(m *testing.M) {
	logger, err := logs.Init("driver-events-test")
	if err != nil {
		panic(err)
	}
	code := m.Run()
	_ = logger.Sync()
	os.Exit(code)
}

type tripConsumerService struct {
	matchCandidate  *repo.Candidate
	matchErr        error
	activeCandidate *repo.Candidate
	activeErr       error
}

func (s *tripConsumerService) RegisterDriver(context.Context, string, string) (*driverpb.Driver, error) {
	return nil, nil
}
func (s *tripConsumerService) UnregisterDriver(context.Context, string) error { return nil }
func (s *tripConsumerService) UpdateLocation(context.Context, string, float64, float64) (string, error) {
	return "", nil
}
func (s *tripConsumerService) MatchAndReserve(context.Context, *trippb.Trip, []byte) (*repo.Candidate, error) {
	return s.matchCandidate, s.matchErr
}
func (s *tripConsumerService) ActiveOffer(context.Context, string) (*repo.Candidate, error) {
	return s.activeCandidate, s.activeErr
}
func (s *tripConsumerService) Decline(context.Context, string, string) (*repo.RetryTrip, error) {
	return nil, nil
}
func (s *tripConsumerService) ExpireOffers(context.Context) ([]repo.RetryTrip, error) {
	return nil, nil
}

type recordedExpiry struct {
	tripID  string
	attempt int
	at      time.Time
}

func (s *recordedExpiry) ScheduleOfferExpiry(_ context.Context, tripID string, attempt int, at time.Time) error {
	s.tripID, s.attempt, s.at = tripID, attempt, at
	return nil
}

func tripCreatedMessage(t *testing.T) *pubsub.Message {
	t.Helper()
	payload, err := json.Marshal(messaging.TripEventData{Trip: &trippb.Trip{Id: "trip-1"}})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(contracts.EventMessage{Version: contracts.EventSchemaVersion, EventID: "event-1", EntityID: "rider-1", Data: payload})
	if err != nil {
		t.Fatal(err)
	}
	return &pubsub.Message{Topic: contracts.TripEventCreated, Data: envelope}
}

func TestTripConsumerSchedulesReservedOfferExpiry(t *testing.T) {
	expiresAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := &tripConsumerService{matchCandidate: &repo.Candidate{Driver: &driverpb.Driver{Id: "driver-1"}, Attempt: 2, ExpiresAt: expiresAt}}
	scheduled := &recordedExpiry{}
	consumer := NewTripConsumer(nil, svc, scheduled)

	if err := consumer.Handle(context.Background(), tripCreatedMessage(t)); err != nil {
		t.Fatal(err)
	}
	if scheduled.tripID != "trip-1" || scheduled.attempt != 2 || !scheduled.at.Equal(expiresAt.Add(time.Second)) {
		t.Fatalf("unexpected expiry task: %#v", scheduled)
	}
}

func TestTripConsumerReschedulesExistingOfferAfterRetry(t *testing.T) {
	expiresAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := &tripConsumerService{
		matchErr:        repo.ErrTripAlreadyMatched,
		activeCandidate: &repo.Candidate{Driver: &driverpb.Driver{Id: "driver-1"}, Attempt: 3, ExpiresAt: expiresAt},
	}
	scheduled := &recordedExpiry{}
	consumer := NewTripConsumer(nil, svc, scheduled)

	if err := consumer.Handle(context.Background(), tripCreatedMessage(t)); err != nil {
		t.Fatal(err)
	}
	if scheduled.attempt != 3 || !scheduled.at.Equal(expiresAt.Add(time.Second)) {
		t.Fatalf("unexpected retry expiry task: %#v", scheduled)
	}
}

func TestTripConsumerReturnsSchedulingFailureForPubSubRetry(t *testing.T) {
	svc := &tripConsumerService{matchCandidate: &repo.Candidate{Driver: &driverpb.Driver{Id: "driver-1"}, ExpiresAt: time.Now()}}
	consumer := NewTripConsumer(nil, svc, expirySchedulerFunc(func(context.Context, string, int, time.Time) error {
		return errors.New("tasks unavailable")
	}))
	if err := consumer.Handle(context.Background(), tripCreatedMessage(t)); err == nil {
		t.Fatal("expected scheduling failure")
	}
}

type expirySchedulerFunc func(context.Context, string, int, time.Time) error

func (f expirySchedulerFunc) ScheduleOfferExpiry(ctx context.Context, tripID string, attempt int, at time.Time) error {
	return f(ctx, tripID, attempt, at)
}
