package handler

import (
	"testing"
	"time"
)

func TestAmendmentTiming(t *testing.T) {
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                     string
		current, proposed, delta float64
	}{
		{"longer", 600, 1140, 540},
		{"shorter", 600, 360, -240},
		{"same", 600, 600, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := amendmentTiming(at, "started", tc.current, tc.proposed)
			if got["deltaDurationSeconds"] != tc.delta || got["currentDurationSeconds"] != tc.current || got["proposedDurationSeconds"] != tc.proposed {
				t.Fatalf("incorrect durations: %v", got)
			}
			if got["currentEstimatedArrivalAt"] != at.Add(time.Duration(tc.current)*time.Second) || got["proposedEstimatedArrivalAt"] != at.Add(time.Duration(tc.proposed)*time.Second) || got["estimatedAt"] != at {
				t.Fatalf("inconsistent arrival baseline: %v", got)
			}
			if got["basis"] != "driver_location" || got["includesStopWaitingTime"] != false {
				t.Fatalf("incorrect basis: %v", got)
			}
		})
	}
	for _, status := range []string{"accepted", "arrived"} {
		got := amendmentTiming(at, status, 600, 1140)
		if got["basis"] != "pickup_departure" || got["currentEstimatedArrivalAt"] != nil || got["proposedEstimatedArrivalAt"] != nil {
			t.Fatalf("pre-pickup arrival must not imply approach time is included: %v", got)
		}
	}
}
