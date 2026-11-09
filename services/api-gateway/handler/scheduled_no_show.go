package handler

import "time"

// Arriving early never starts a scheduled Rider's no-show clock early.
func scheduledNoShowDeadline(arrived time.Time, pickup *time.Time, waitMinutes int) time.Time {
	if pickup != nil && pickup.After(arrived) {
		arrived = *pickup
	}
	return arrived.Add(time.Duration(waitMinutes) * time.Minute)
}
