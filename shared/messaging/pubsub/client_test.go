package pubsub

import (
	"context"
	"testing"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/observe/correlation"
)

func TestSubscriptionIDMatchesTerraformNaming(t *testing.T) {
	got := SubscriptionID("trip-service-group", "trip.cmd.complete")
	if got != "trip-service-group-trip-cmd-complete" {
		t.Fatalf("SubscriptionID() = %q", got)
	}
}

func TestPrepareCreatesStableEnvelopeMetadata(t *testing.T) {
	ctx := correlation.WithID(context.Background(), "correlation-1")
	message := &contracts.EventMessage{EntityID: "trip-1"}
	prepare(ctx, message)
	if message.Version != contracts.EventSchemaVersion || message.EventID == "" || message.CorrelationID != "correlation-1" {
		t.Fatalf("unexpected prepared message: %#v", message)
	}

	eventID := message.EventID
	prepare(context.Background(), message)
	if message.EventID != eventID || message.CorrelationID != "correlation-1" {
		t.Fatalf("prepare changed stable metadata: %#v", message)
	}
}
