package repo

import (
	"context"
	"testing"

	triptypes "github.com/luxipha/heyGo_backend/services/trip-service/types"
	tripproto "github.com/luxipha/heyGo_backend/shared/proto/trip"
	"github.com/google/uuid"
)

func TestDriverStartIsIdempotent(t *testing.T) {
	repository := NewInMemoRepository()
	tripID := uuid.NewString()
	_, err := repository.Create(context.Background(), &triptypes.TripModel{
		ID: tripID, RiderID: "rider-1", Status: "accepted",
		RideFare: &triptypes.RideFareModel{ID: uuid.NewString(), Route: &triptypes.OSRMApiResponse{}},
		Driver:   &tripproto.TripDriver{Id: "driver-1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	trip, transitioned, err := repository.Arrive(context.Background(), tripID, "driver-1")
	if err != nil || !transitioned || trip.Status != "arrived" {
		t.Fatalf("arrival = status %q, transitioned %v, error %v", trip.Status, transitioned, err)
	}
	trip, transitioned, err = repository.Arrive(context.Background(), tripID, "driver-1")
	if err != nil || transitioned || trip.Status != "arrived" {
		t.Fatalf("duplicate arrival = status %q, transitioned %v, error %v", trip.Status, transitioned, err)
	}
	trip, transitioned, err = repository.Start(context.Background(), tripID, "driver-1")
	if err != nil || !transitioned || trip.Status != "started" {
		t.Fatalf("first transition = status %q, transitioned %v, error %v", trip.Status, transitioned, err)
	}
	trip, transitioned, err = repository.Start(context.Background(), tripID, "driver-1")
	if err != nil || transitioned || trip.Status != "started" {
		t.Fatalf("duplicate transition = status %q, transitioned %v, error %v", trip.Status, transitioned, err)
	}
}

func TestDriverStartRejectsInvalidStateAndActor(t *testing.T) {
	repository := NewInMemoRepository()
	tripID := uuid.NewString()
	_, _ = repository.Create(context.Background(), &triptypes.TripModel{ID: tripID, Status: "pending", Driver: &tripproto.TripDriver{Id: "driver-1"}})
	if _, _, err := repository.Start(context.Background(), tripID, "driver-1"); err == nil {
		t.Fatal("pending trip started before arrival")
	}
	acceptedID := uuid.NewString()
	_, _ = repository.Create(context.Background(), &triptypes.TripModel{
		ID: acceptedID, Status: "accepted", Driver: &tripproto.TripDriver{Id: "driver-1"},
	})
	if _, _, err := repository.Start(context.Background(), acceptedID, "other-driver"); err == nil {
		t.Fatal("unassigned driver started trip")
	}
	if _, changed, err := repository.Start(context.Background(), acceptedID, "driver-1"); err != nil || !changed {
		t.Fatalf("assigned driver should be able to explicitly start: changed=%v err=%v", changed, err)
	}
}

func TestCompleteAndCancelEnforceParticipantAndState(t *testing.T) {
	repository := NewInMemoRepository()
	startedID := uuid.NewString()
	_, _ = repository.Create(context.Background(), &triptypes.TripModel{ID: startedID, RiderID: "rider-1", Status: "started", RideFare: &triptypes.RideFareModel{ID: uuid.NewString(), Route: &triptypes.OSRMApiResponse{}}, Driver: &tripproto.TripDriver{Id: "driver-1"}})
	if _, _, err := repository.Complete(context.Background(), startedID, "other-driver"); err == nil {
		t.Fatal("unassigned driver completed trip")
	}
	trip, changed, err := repository.Complete(context.Background(), startedID, "driver-1")
	if err != nil || !changed || trip.Status != "completed" {
		t.Fatalf("complete transition failed: trip=%+v changed=%v err=%v", trip, changed, err)
	}
	if _, changed, err := repository.Complete(context.Background(), startedID, "driver-1"); err != nil || changed {
		t.Fatalf("duplicate completion was not idempotent: changed=%v err=%v", changed, err)
	}

	pendingID := uuid.NewString()
	_, _ = repository.Create(context.Background(), &triptypes.TripModel{ID: pendingID, RiderID: "rider-1", Status: "pending", RideFare: &triptypes.RideFareModel{ID: uuid.NewString(), Route: &triptypes.OSRMApiResponse{}}, Driver: &tripproto.TripDriver{}})
	if _, _, err := repository.Cancel(context.Background(), pendingID, "other-rider", "changed mind"); err == nil {
		t.Fatal("non-participant cancelled trip")
	}
	trip, changed, err = repository.Cancel(context.Background(), pendingID, "rider-1", "changed mind")
	if err != nil || !changed || trip.Status != "cancelled" {
		t.Fatalf("cancel transition failed: trip=%+v changed=%v err=%v", trip, changed, err)
	}
}
