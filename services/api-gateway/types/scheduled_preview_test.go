package types

import (
	"testing"
	"time"
)

func TestScheduledPreviewPickupTime(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		scheduled bool
		at        string
		valid     bool
	}{
		{"immediate", false, "", true},
		{"scheduled", true, "2026-11-09T10:00:00+01:00", true},
		{"missing", true, "", false},
		{"no timezone", true, "2026-11-09T10:00:00", false},
		{"past", true, "2026-11-08T11:00:00Z", false},
		{"now", true, "2026-11-08T12:00:00Z", false},
		{"unexpected", false, "2026-11-09T10:00:00Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := PreviewTripRequest{ForScheduled: tc.scheduled, PickupAt: tc.at}
			if (r.ValidatePickupTime(now) == nil) != tc.valid {
				t.Fatal("validation mismatch")
			}
			if tc.valid && r.ToProto("rider").GetPickupAt() != tc.at {
				t.Fatal("pickupAt lost across RPC")
			}
		})
	}
}
