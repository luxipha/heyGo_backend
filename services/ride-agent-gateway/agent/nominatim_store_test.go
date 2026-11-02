package agent

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresNominatimBudgetAndCache(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := fmt.Sprintf("nominatim_test_%d", time.Now().UnixNano())
	name := pgx.Identifier{schema}.Sanitize()
	if _, err = root.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(ctx, "DROP SCHEMA "+name+" CASCADE")
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	migration, err := os.ReadFile("../../../shared/db/migrations/094_nominatim_places.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	store := PostgresNominatimStore{Pool: pool}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if store.Reserve(ctx, "upstream") == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent requests escaped shared limit: %d", successes.Load())
	}
	if _, err = pool.Exec(ctx, `UPDATE geo_place_upstreams SET next_request_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err = store.Reserve(ctx, "upstream"); err != nil {
		t.Fatal(err)
	}
	if err = store.Backoff(ctx, "upstream"); err != nil {
		t.Fatal(err)
	}
	if err = store.Reserve(ctx, "upstream"); err == nil {
		t.Fatal("provider backoff bypassed")
	}
	places := []Place{{ID: "osm_N_123", Name: "Public place", Provider: "nominatim", Location: Coordinate{6.45, 3.45}}}
	if err = store.Write(ctx, "query-key", places); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Read(ctx, "query-key")
	if err != nil || !ok || len(got) != 1 || got[0].ID != places[0].ID {
		t.Fatalf("cache not shared/persistent %+v %v", got, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE geo_place_cache SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = store.Read(ctx, "query-key"); err != nil || ok {
		t.Fatal("expired cache served")
	}
	// Saved references resolve with their originating provider after a switch.
	if _, err = pool.Exec(ctx, `CREATE TABLE admin_geo_settings(id integer PRIMARY KEY,configuration jsonb,version bigint); INSERT INTO admin_geo_settings VALUES(1,'{"placeProvider":"nominatim"}',1)`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIDE_AGENT_PLACES_KEY", "synthetic-key")
	t.Setenv("NOMINATIM_BASE_URL", "https://nominatim.openstreetmap.org")
	router := DatabasePlaces{Pool: pool}
	provider, err := router.provider(ctx, "")
	if _, ok := provider.(NominatimPlaces); err != nil || !ok {
		t.Fatal("Admin Nominatim choice ignored")
	}
	provider, err = router.provider(ctx, "google-reference")
	if _, ok := provider.(GooglePlaces); err != nil || !ok {
		t.Fatal("Google reference routed to OSM")
	}
	if _, err = pool.Exec(ctx, `UPDATE admin_geo_settings SET configuration='{"placeProvider":"google"}'`); err != nil {
		t.Fatal(err)
	}
	provider, err = router.provider(ctx, "osm_W_123")
	if _, ok := provider.(NominatimPlaces); err != nil || !ok {
		t.Fatal("saved OSM reference lost after provider switch")
	}
	if _, err = pool.Exec(ctx, `UPDATE admin_geo_settings SET configuration='{"placeProvider":"disabled"}'`); err != nil {
		t.Fatal(err)
	}
	if _, err = router.provider(ctx, "osm_W_123"); err == nil {
		t.Fatal("disabled search bypassed")
	}
}
