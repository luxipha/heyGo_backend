package handler

import (
	"testing"
	"time"
)

func TestEarningDayBoundsUseConfiguredCalendarDay(t *testing.T) {
	location, err := time.LoadLocation(driverEarningsTimeZone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 23, 30, 0, 0, time.UTC)
	start, end, date := earningDayBounds(now, location)
	if date != "2026-09-25" || !start.Equal(time.Date(2026, 9, 24, 23, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("date=%s start=%s end=%s", date, start, end)
	}
}
