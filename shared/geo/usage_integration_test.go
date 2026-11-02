package geo

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"sync"
	"testing"
	"time"
)

func TestRoutingUsageDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("test_geo_usage_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	migration, err := os.ReadFile("../db/migrations/096_geo_route_usage.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	usage, err := ReadRouteUsage(ctx, pool)
	if err != nil || len(usage.Providers) != 3 || usage.FirstTrackedAt != nil {
		t.Fatalf("empty usage %+v %v", usage, err)
	}
	recorder := databaseRouteUsage{Pool: pool}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			day, err := recorder.Begin(ctx, "google")
			if err != nil {
				t.Error(err)
				return
			}
			if err = recorder.Finish(ctx, "google", day, i%2 == 0); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	_, err = pool.Exec(ctx, `INSERT INTO geo_route_daily_usage(day,provider,attempts) VALUES ((DATE_TRUNC('month',NOW() AT TIME ZONE 'UTC')-INTERVAL '1 day')::DATE,'google',9000)`)
	if err != nil {
		t.Fatal(err)
	}
	usage, err = ReadRouteUsage(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	r := usage.Providers[0]
	if r.Attempts != 30 || r.Successes != 15 || r.Failures != 15 || r.Pending != 0 || r.Remaining != 4970 || usage.FirstTrackedAt == nil {
		t.Fatalf("incorrect concurrent/monthly counts %+v", usage)
	}
}
