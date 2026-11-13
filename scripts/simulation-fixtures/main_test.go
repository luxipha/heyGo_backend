package main

import "testing"

func TestGuardRejectsCloudAndConnectionOverrides(t *testing.T) {
	good := "postgres://fixture@127.0.0.1:55439/heygo_rider_staging?sslmode=disable"
	for _, raw := range []string{"postgres://fixture@cloud.example/heygo_rider_staging", "postgres://fixture@127.0.0.1:5432/heygo_rider_staging", good + "&host=production.example", good + "&service=production", "postgres://fixture@localhost:55439/heygo_rider_staging"} {
		if guard(raw, "heygo-admin-local", "127.0.0.1:8086") == nil {
			t.Fatalf("unsafe database accepted: %s", raw)
		}
	}
	if guard(good, "heygo-ng", "127.0.0.1:8086") == nil {
		t.Fatal("production project accepted")
	}
	if guard(good, "heygo-admin-local", "") == nil {
		t.Fatal("missing emulator accepted")
	}
	if err := guard(good, "heygo-admin-local", "127.0.0.1:8086"); err != nil {
		t.Fatal(err)
	}
}
