package pubsub

import (
	"context"
	"testing"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/observe/correlation"
)

func TestSubscriptionIDMatchesTerraformNaming(t *testing.T) {
	// Explicit expected IDs match infra/terraform/messaging.tf and the existing
	// cloud resources. Cover every current service/topic pair, including all
	// underscore-bearing topics; do not derive expectations with SubscriptionID.
	cases := []struct {
		groupID, topic, want string
	}{
		{"api-gateway-group", contracts.DriverCmdTripRequest, "api-gateway-group-driver-cmd-trip_request"},
		{"api-gateway-group", contracts.DriverEventCommandAcknowledged, "api-gateway-group-driver-event-command_acknowledged"},
		{"api-gateway-group", contracts.DriverEventLocationUpdated, "api-gateway-group-driver-event-location_updated"},
		{"api-gateway-group", contracts.PaymentEventCancelled, "api-gateway-group-payment-event-cancelled"},
		{"api-gateway-group", contracts.PaymentEventFailed, "api-gateway-group-payment-event-failed"},
		{"api-gateway-group", contracts.PaymentEventSessionCreated, "api-gateway-group-payment-event-session_created"},
		{"api-gateway-group", contracts.PaymentEventSuccess, "api-gateway-group-payment-event-success"},
		{"api-gateway-group", contracts.TripEventArrived, "api-gateway-group-trip-event-arrived"},
		{"api-gateway-group", contracts.TripEventCancelled, "api-gateway-group-trip-event-cancelled"},
		{"api-gateway-group", contracts.TripEventCompleted, "api-gateway-group-trip-event-completed"},
		{"api-gateway-group", contracts.TripEventCreated, "api-gateway-group-trip-event-created"},
		{"api-gateway-group", contracts.TripEventDriverAssigned, "api-gateway-group-trip-event-driver_assigned"},
		{"api-gateway-group", contracts.TripEventExpired, "api-gateway-group-trip-event-expired"},
		{"api-gateway-group", contracts.TripEventNoDriversFound, "api-gateway-group-trip-event-no_drivers_found"},
		{"api-gateway-group", contracts.TripEventNoShowClaimUpdated, "api-gateway-group-trip-event-no_show_claim_updated"},
		{"api-gateway-group", contracts.TripEventReassigned, "api-gateway-group-trip-event-reassigned"},
		{"api-gateway-group", contracts.TripEventSettlementUpdated, "api-gateway-group-trip-event-settlement_updated"},
		{"api-gateway-group", contracts.TripEventStarted, "api-gateway-group-trip-event-started"},
		{"driver-service-group", contracts.DriverCmdLocation, "driver-service-group-driver-cmd-location"},
		{"driver-service-group", contracts.DriverCmdTripDecline, "driver-service-group-driver-cmd-trip_decline"},
		{"driver-service-group", contracts.TripEventCreated, "driver-service-group-trip-event-created"},
		{"driver-service-group", contracts.TripEventDriverNotInterested, "driver-service-group-trip-event-driver_not_interested"},
		{"payment-service-group", contracts.PaymentCmdCreateSession, "payment-service-group-payment-cmd-create_session"},
		{"trip-service-group", contracts.DriverCmdTripAccept, "trip-service-group-driver-cmd-trip_accept"},
		{"trip-service-group", contracts.TripCmdArrive, "trip-service-group-trip-cmd-arrive"},
		{"trip-service-group", contracts.TripCmdCancel, "trip-service-group-trip-cmd-cancel"},
		{"trip-service-group", contracts.TripCmdComplete, "trip-service-group-trip-cmd-complete"},
		{"trip-service-group", contracts.TripCmdRate, "trip-service-group-trip-cmd-rate"},
		{"trip-service-group", contracts.TripCmdStart, "trip-service-group-trip-cmd-start"},
		// Terraform preserves the group ID verbatim; only topic dots change.
		{"custom.group_with_underscore", contracts.PaymentCmdCreateSession, "custom.group_with_underscore-payment-cmd-create_session"},
	}
	seen := make(map[string]bool)
	for _, tc := range cases {
		t.Run(tc.groupID+"/"+tc.topic, func(t *testing.T) {
			got := SubscriptionID(tc.groupID, tc.topic)
			if got != tc.want {
				t.Fatalf("SubscriptionID(%q, %q) = %q, want Terraform ID %q", tc.groupID, tc.topic, got, tc.want)
			}
			if seen[got] {
				t.Fatalf("subscription ID collision: %q", got)
			}
			seen[got] = true
		})
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
