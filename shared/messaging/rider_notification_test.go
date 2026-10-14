package messaging

import (
	"encoding/json"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"strings"
	"testing"
)

func TestRiderNotificationsAllowlistTargetsAndSuppressSensitiveEvents(t *testing.T) {
	raw := json.RawMessage(`{"trip":{"id":"11111111-1111-4111-8111-111111111111","pickup":{"latitude":6.5},"riderPhone":"private"},"driver":{"phone":"private"},"amountKobo":12345}`)
	for _, kind := range []string{contracts.TripEventCreated, contracts.TripEventDriverAssigned, contracts.TripEventArrived, contracts.TripEventStarted, contracts.TripEventCompleted, contracts.TripEventCancelled, contracts.TripEventNoDriversFound, contracts.TripEventSettlementUpdated, contracts.TripEventNoShowClaimUpdated, contracts.TripEventReassigned} {
		n := riderNotificationForEvent(kind, raw)
		if n == nil {
			t.Fatalf("missing %s", kind)
		}
		if strings.Contains(string(n.Data), "private") || strings.Contains(string(n.Data), "latitude") || strings.Contains(string(n.Data), "amount") {
			t.Fatalf("sensitive notification %s", n.Data)
		}
	}
	for _, kind := range []string{contracts.DriverEventLocationUpdated, "driver.document.updated", "trip.message.read", "trip.event.command_result"} {
		if riderNotificationForEvent(kind, raw) != nil {
			t.Fatalf("unexpected event %s", kind)
		}
	}
	if riderNotificationForEvent(contracts.TripEventCompleted, json.RawMessage(`{"id":"invalid"}`)) != nil {
		t.Fatal("invalid target accepted")
	}
}
