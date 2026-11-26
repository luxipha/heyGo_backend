package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	declineErr      error
	declineCalls    int
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
	s.declineCalls++
	return nil, s.declineErr
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

func declineMessage(t *testing.T) *pubsub.Message {
	t.Helper()
	payload, err := json.Marshal(messaging.DriverTripResponseData{TripID: "trip-1"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(contracts.EventMessage{Version: contracts.EventSchemaVersion, EventID: "event-2", EntityID: "driver-1", Data: payload})
	if err != nil {
		t.Fatal(err)
	}
	return &pubsub.Message{Topic: contracts.DriverCmdTripDecline, Data: envelope}
}

// A decline can reach the service after the offer already expired (the
// delivery can lag by more than the 20 s offer window), was accepted elsewhere,
// or was already applied once. Nothing is left to undo, and retrying can never
// succeed, so the message must be acknowledged. Returning the error made
// Pub/Sub redeliver it endlessly (61,791 failures in one QA session).
func TestTripConsumerAcknowledgesDeclineForResolvedAssignment(t *testing.T) {
	svc := &tripConsumerService{declineErr: repo.ErrAssignmentNotActive}
	consumer := NewTripConsumer(nil, svc)

	if err := consumer.Handle(context.Background(), declineMessage(t)); err != nil {
		t.Fatalf("a decline for a resolved assignment must be acknowledged, got %v", err)
	}
	if svc.declineCalls != 1 {
		t.Fatalf("decline was not attempted: %d", svc.declineCalls)
	}
}

func TestTripConsumerAcknowledgesWrappedResolvedAssignment(t *testing.T) {
	svc := &tripConsumerService{declineErr: fmt.Errorf("decline: %w", repo.ErrAssignmentNotActive)}
	if err := NewTripConsumer(nil, svc).Handle(context.Background(), declineMessage(t)); err != nil {
		t.Fatalf("wrapped sentinel must also be acknowledged, got %v", err)
	}
}

// A real failure (database down) is transient: it must still be retried.
func TestTripConsumerStillRetriesTransientDeclineFailure(t *testing.T) {
	svc := &tripConsumerService{declineErr: errors.New("connection refused")}
	if err := NewTripConsumer(nil, svc).Handle(context.Background(), declineMessage(t)); err == nil {
		t.Fatal("a transient failure must be returned so Pub/Sub retries it")
	}
}
