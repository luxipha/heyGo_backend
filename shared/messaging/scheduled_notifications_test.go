package messaging

import (
	"encoding/json"
	"testing"
)

func TestScheduledNotificationAudienceAndNavigation(t *testing.T) {
	driver := json.RawMessage(`{"bookingId":"00000000-0000-4000-8000-000000000001","audience":"driver"}`)
	rider := json.RawMessage(`{"bookingId":"00000000-0000-4000-8000-000000000001","audience":"rider"}`)
	n := notificationForDriverEvent("scheduled.event.offer", driver)
	if n == nil || n.Type != "dispatch" || n.Title != "Scheduled ride available" {
		t.Fatal(n)
	}
	if riderNotificationForEvent("scheduled.event.offer", driver) != nil {
		t.Fatal("driver event created Rider inbox item")
	}
	if notificationForDriverEvent("scheduled.event.accept", rider) != nil {
		t.Fatal("rider event created Driver inbox item")
	}
	n = riderNotificationForEvent("scheduled.event.accept", rider)
	if n == nil {
		t.Fatal("missing rider notification")
	}
	var data struct {
		Target struct {
			Kind string `json:"kind"`
		} `json:"target"`
	}
	if json.Unmarshal(n.Data, &data) != nil || data.Target.Kind != "scheduled_ride" {
		t.Fatal(string(n.Data))
	}
}
