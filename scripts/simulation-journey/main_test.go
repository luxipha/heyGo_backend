package main

import (
	"os"
	"testing"
)

func TestGuardRefusesProductionAndConnectionOverrides(t *testing.T) {
	t.Setenv("GCP_PROJECT_ID", "heygo-admin-local")
	t.Setenv("PUBSUB_EMULATOR_HOST", "127.0.0.1:8086")
	for _, dsn := range []string{"postgres://local@cloud.example/heygo_rider_staging", "postgres://local@127.0.0.1:55439/production", "postgres://local@127.0.0.1:55439/heygo_rider_staging?host=cloud.example"} {
		t.Setenv("DATABASE_URL", dsn)
		if guard() == nil {
			t.Fatal("unsafe connection allowed")
		}
	}
	t.Setenv("DATABASE_URL", "postgres://local@127.0.0.1:55439/heygo_rider_staging?sslmode=disable")
	if e := guard(); e != nil {
		t.Fatal(e)
	}
	os.Setenv("GCP_PROJECT_ID", "production")
	if guard() == nil {
		t.Fatal("cloud project allowed")
	}
}
