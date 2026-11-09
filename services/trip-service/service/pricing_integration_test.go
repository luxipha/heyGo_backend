package service

import (
	"context"
	"fmt"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/services/trip-service/repo"
	triptypes "github.com/luxipha/heyGo_backend/services/trip-service/types"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
)

func TestQuoteUsesPublishedMarketPricingAndPersistsVersion(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	creator, approver, rider, zone, pricing := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	market := "QUOTE_" + strings.ToUpper(zone[:8])
	seed, _ := strconv.ParseUint(zone[:4], 16, 16)
	seedY, _ := strconv.ParseUint(zone[4:8], 16, 16)
	x, y := 70+float64(seed)/100000, 10+float64(seedY)/100000
	boundary := fmt.Sprintf("MULTIPOLYGON(((%f %f,%f %f,%f %f,%f %f,%f %f)))", x, y, x+0.001, y, x+0.001, y+0.001, x, y+0.001, x, y)
	for _, id := range []string{creator, approver} {
		if _, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'test')`, id, "quote-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['rider'])`, rider, "quote:"+rider, "quote:"+rider); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at)
		VALUES($1::UUID,'market',$2,$2,1,ST_GeomFromText($5,4326),'approved',NOW()-INTERVAL '1 day',$3::UUID,$4::UUID,NOW())`, zone, market, creator, approver, boundary); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO pricing_versions(id,market_code,version,status,effective_from,created_by,approved_by,approved_at) VALUES($1::UUID,$2,1,'approved',NOW()-INTERVAL '1 day',$3::UUID,$4::UUID,NOW())`, pricing, market, creator, approver); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"bike", "auto", "sedan", "suv"} {
		if _, err = pool.Exec(ctx, `INSERT INTO pricing_package_rates(pricing_id,package_slug,base_kobo,per_km_kobo,per_minute_kobo) VALUES($1::UUID,$2,100,200,60)`, pricing, pkg); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(repo.NewPostgresRepository(pool))
	route := &triptypes.OSRMApiResponse{}
	route.Routes = append(route.Routes, geo.Route{Distance: 1000, Duration: 300})
	fares, err := service.GenerateTripFares(ctx, rider, route, &sharedtypes.Coordinate{Latitude: y + 0.0005, Longitude: x + 0.0005}, &sharedtypes.Coordinate{Latitude: y + 0.0006, Longitude: x + 0.0006})
	if err != nil {
		t.Fatal(err)
	}
	if len(fares) != 4 || fares[0].TotalFareInPaise != 600 || fares[0].PricingVersionID != pricing || fares[0].PricingMarketCode != market {
		t.Fatalf("quoted fares=%+v", fares)
	}
	group := fares[0].PreviewID
	if group == "" {
		t.Fatal("missing preview identity")
	}
	for _, fare := range fares {
		if fare.PreviewID != group || !fare.ExpiresAt.After(time.Now()) {
			t.Fatalf("invalid grouped quote: %+v", fare)
		}
	}
	var frozen string
	if err = pool.QueryRow(ctx, `SELECT pricing_version_id::TEXT FROM ride_fares WHERE id=$1::UUID`, fares[0].ID).Scan(&frozen); err != nil || frozen != pricing {
		t.Fatalf("frozen=%s err=%v", frozen, err)
	}

	// Two later pickups straddling a published price change must quote different rates.
	boundaryAt := time.Now().UTC().Add(4 * time.Hour).Truncate(time.Second)
	futurePricing := uuid.NewString()
	if _, err = pool.Exec(ctx, `UPDATE pricing_versions SET effective_until=$2 WHERE id=$1::uuid`, pricing, boundaryAt); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO pricing_versions(id,market_code,version,status,effective_from,created_by,approved_by,approved_at) VALUES($1::uuid,$2,2,'approved',$3,$4::uuid,$5::uuid,NOW())`, futurePricing, market, boundaryAt, creator, approver); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"bike", "auto", "sedan", "suv"} {
		if _, err = pool.Exec(ctx, `INSERT INTO pricing_package_rates(pricing_id,package_slug,base_kobo,per_km_kobo,per_minute_kobo) VALUES($1::uuid,$2,200,400,120)`, futurePricing, pkg); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		at      time.Time
		version string
		amount  float64
	}{
		{boundaryAt.Add(-time.Second), pricing, 600},
		{boundaryAt, futurePricing, 1200},
		{boundaryAt.Add(time.Hour), futurePricing, 1200},
	} {
		quoted, e := service.GenerateTripFares(ctx, rider, route, &sharedtypes.Coordinate{Latitude: y + 0.0005, Longitude: x + 0.0005}, &sharedtypes.Coordinate{Latitude: y + 0.0006, Longitude: x + 0.0006}, tc.at)
		if e != nil {
			t.Fatal(e)
		}
		if len(quoted) != 4 || quoted[0].PricingVersionID != tc.version || quoted[0].TotalFareInPaise != tc.amount {
			t.Fatalf("pickup %v: %+v", tc.at, quoted)
		}
	}

	if _, err = pool.Exec(ctx, `UPDATE users SET email=$2,email_verified=true,phone_e164=$3,phone_verified_at=NOW() WHERE id=$1::uuid`, rider, rider+"@example.test", "+999"+fmt.Sprintf("%012d", seed)); err != nil {
		t.Fatal(err)
	}
	futureFares, e := service.GenerateTripFares(ctx, rider, route, &sharedtypes.Coordinate{Latitude: y + 0.0005, Longitude: x + 0.0005}, &sharedtypes.Coordinate{Latitude: y + 0.0006, Longitude: x + 0.0006}, boundaryAt)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.CreateTrip(ctx, futureFares[0]); e != repo.ErrFareExpired {
		t.Fatalf("future pricing used for immediate trip: %v", e)
	}
	var retained string
	if err = pool.QueryRow(ctx, `SELECT pricing_version_id::text FROM ride_fares WHERE id=$1::uuid`, fares[0].ID).Scan(&retained); err != nil || retained != pricing {
		t.Fatal("old quote mutated", retained, err)
	}
	_, err = service.GenerateTripFares(ctx, rider, route, &sharedtypes.Coordinate{Latitude: 1, Longitude: 1}, nil)
	if err != repo.ErrPricingUnavailable {
		t.Fatalf("expected fail-closed quote, got %v", err)
	}
}
