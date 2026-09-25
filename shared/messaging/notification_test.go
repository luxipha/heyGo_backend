package messaging

import (
	"encoding/json"
	"testing"

	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func TestNotificationForDriverBusinessEvents(t *testing.T) {
	got := notificationForDriverEvent(contracts.TripEventSettlementUpdated, json.RawMessage(`{"status":"confirmed","expectedAmountKobo":500025}`))
	if got == nil || got.Title != "Trip payment updated" || got.Message != "Trip payment status: Confirmed (₦5000.25)." {
		t.Fatalf("settlement notification=%+v", got)
	}
	got = notificationForDriverEvent("driver.document.updated", json.RawMessage(`{"type":"vehicle_license","status":"rejected","reason":"Upload a clearer photo"}`))
	if got == nil || got.Type != "verification" || got.Message != "Vehicle License status: Rejected. Upload a clearer photo" {
		t.Fatalf("document notification=%+v", got)
	}
	if notificationForDriverEvent(contracts.DriverEventLocationUpdated, json.RawMessage(`{"latitude":6.5,"longitude":3.4}`)) != nil {
		t.Fatal("location updates must not create inbox notifications")
	}
	if notificationForDriverEvent(contracts.DriverEventCommandAcknowledged, json.RawMessage(`{}`)) != nil {
		t.Fatal("command acknowledgements must not create inbox notifications")
	}
}
