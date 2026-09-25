package db

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMigrationsAndPostGIS(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := NewPostgresPool(ctx, Config{URL: url, MaxConnIdleTime: time.Minute, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var postGIS string
	if err := pool.QueryRow(ctx, `SELECT PostGIS_Version()`).Scan(&postGIS); err != nil {
		t.Fatalf("PostGIS unavailable: %v", err)
	}
	for _, table := range []string{"users", "ride_fares", "trips", "drivers", "driver_assignments", "payments", "user_events", "driver_socket_sessions"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.'||$1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s unavailable: exists=%v err=%v", table, exists, err)
		}
	}
}
