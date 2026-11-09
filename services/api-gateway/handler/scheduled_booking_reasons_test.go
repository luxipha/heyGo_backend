package handler

import (
	"strings"
	"testing"
)

func TestScheduledCancellationReasonValidation(t *testing.T) {
	for _, code := range []string{"plans_changed", "wrong_pickup_time", "wrong_route", "booked_by_mistake", "other"} {
		if !validScheduledCancelReason(scheduledCancelReason{Code: code, Note: "A booking reason"}) {
			t.Fatal(code)
		}
	}
	for _, r := range []scheduledCancelReason{{}, {Code: "driver_too_late"}, {Code: "other", Note: strings.Repeat("界", 501)}, {Code: "other", Note: string([]byte{255})}} {
		if validScheduledCancelReason(r) {
			t.Fatal("invalid reason accepted")
		}
	}
}
