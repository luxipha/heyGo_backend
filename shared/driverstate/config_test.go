package driverstate

import (
	"testing"
	"time"
)

func TestConfigEnforced(t *testing.T) {
	now := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	changed := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	before := changed.AddDate(0, 0, -30)
	after := changed.Add(time.Hour)

	cases := []struct {
		name     string
		cfg      Config
		driver   time.Time
		enforced bool
		inGrace  bool
	}{
		{"off never blocks", Config{Mode: ModeOff}, before, false, false},
		{"optional never blocks", Config{Mode: ModeOptional}, before, false, false},
		{"required with no change date blocks", Config{Mode: ModeRequired, GraceDays: 14}, before, true, false},
		{"existing driver inside grace is not blocked", Config{Mode: ModeRequired, GraceDays: 14, RequiredSince: &changed}, before, false, true},
		{"driver registered after the change is blocked at once", Config{Mode: ModeRequired, GraceDays: 14, RequiredSince: &after}, after.Add(time.Hour), true, false},
		{"grace has ended", Config{Mode: ModeRequired, GraceDays: 3, RequiredSince: &changed}, before, true, false},
		{"zero grace blocks at once", Config{Mode: ModeRequired, GraceDays: 0, RequiredSince: &changed}, before, true, false},
	}
	for _, tc := range cases {
		enforced, grace := tc.cfg.Enforced(tc.driver, now)
		if enforced != tc.enforced || (grace != nil) != tc.inGrace {
			t.Errorf("%s: enforced=%v grace=%v, want enforced=%v grace=%v", tc.name, enforced, grace, tc.enforced, tc.inGrace)
		}
	}
	// The grace window ends exactly GraceDays after the change.
	_, until := Config{Mode: ModeRequired, GraceDays: 14, RequiredSince: &changed}.Enforced(before, now)
	if until == nil || !until.Equal(changed.AddDate(0, 0, 14)) {
		t.Errorf("grace end = %v, want %v", until, changed.AddDate(0, 0, 14))
	}
}
