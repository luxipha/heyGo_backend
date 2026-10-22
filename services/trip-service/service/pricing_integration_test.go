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
	_, err = service.GenerateTripFares(ctx, rider, route, &sharedtypes.Coordinate{Latitude: 1, Longitude: 1}, nil)
	if err != repo.ErrPricingUnavailable {
		t.Fatalf("expected fail-closed quote, got %v", err)
	}
}
