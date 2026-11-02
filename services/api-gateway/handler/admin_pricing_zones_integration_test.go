package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	triprepo "github.com/luxipha/heyGo_backend/services/trip-service/repo"
	tripservice "github.com/luxipha/heyGo_backend/services/trip-service/service"
	triptypes "github.com/luxipha/heyGo_backend/services/trip-service/types"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/geo"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
	"github.com/luxipha/heyGo_backend/shared/zonepricing"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestZonePricingDraftApprovalSimulationAndQuote(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	creator, approver, market, zone, category := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	seed, _ := strconv.ParseUint(market[:4], 16, 16)
	x, y := 122+float64(seed)/10000, 33+float64(seed)/100000
	code := "ZTEST_" + strings.ToUpper(market[:8])
	slug := "zone-" + category
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO admin_users(id,username,password_hash) VALUES($1::uuid,$1,'test'),($2::uuid,$2,'test')`, creator, approver)
	// Unique, remote test polygons do not overlap local Lagos configuration.
	for i, id := range []string{market, zone} {
		kind := "market"
		bounds := fmt.Sprintf("MULTIPOLYGON(((%f %f,%f %f,%f %f,%f %f,%f %f)))", x, y, x+0.1, y, x+0.1, y+0.1, x, y+0.1, x, y)
		zcode := code
		if i == 1 {
			kind = "region"
			bounds = fmt.Sprintf("MULTIPOLYGON(((%f %f,%f %f,%f %f,%f %f,%f %f)))", x+0.01, y+0.01, x+0.02, y+0.01, x+0.02, y+0.02, x+0.01, y+0.02, x+0.01, y+0.01)
			zcode += "_PICKUP"
		}
		exec(`INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at) VALUES($1::uuid,$2,$3,$3,1,ST_GeomFromText($4,4326),'approved',NOW()-interval '1 day',$5::uuid,$6::uuid,NOW())`, id, kind, zcode, bounds, creator, approver)
	}
	exec(`INSERT INTO ride_categories(id,slug,name,passengers,status) VALUES($1::uuid,$2,$2,4,'active')`, category, slug)
	// Published test fixtures retain history and are retired after the assertion window.
	defer func() {
		if _, e := pool.Exec(context.Background(), `UPDATE ride_categories SET status='archived' WHERE id=$1::uuid`, category); e != nil {
			t.Error(e)
		}
	}()
	defer func() {
		_, e := pool.Exec(context.Background(), `UPDATE pricing_versions SET effective_until=NOW()+INTERVAL '1 second' WHERE market_code=$1 AND status='approved' AND effective_until IS NULL`, code)
		if e != nil {
			t.Error(e)
		}
		_, e = pool.Exec(context.Background(), `UPDATE trip_geofences SET effective_until=NOW()+INTERVAL '1 second' WHERE id IN($1::uuid,$2::uuid) AND effective_until IS NULL`, market, zone)
		if e != nil {
			t.Error(e)
		}
	}()

	api := &adminAPI{pool: pool}
	call := func(actor, id string, h gin.HandlerFunc, body any, want int) map[string]json.RawMessage {
		t.Helper()
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/admin/test", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: id}}
		c.Set("admin", adminIdentity{ID: actor, Username: "staff"})
		h(c)
		if w.Code != want {
			t.Fatalf("want %d got %d: %s", want, w.Code, w.Body.String())
		}
		out := map[string]json.RawMessage{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	rates := []pricingRateInput{{PackageSlug: slug, BaseKobo: 100, PerKmKobo: 200, PerMinuteKobo: 60}}
	body := map[string]any{"marketCode": code, "effectiveFrom": time.Now().UTC().Add(-time.Minute), "rates": rates, "zones": []zonepricing.Rule{{ZoneID: zone, Kind: "multiplier", Value: 12500}}}
	created := call(creator, "", api.createPricing, body, 201)
	var data struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(created["data"], &data)
	if data.ID == "" {
		t.Fatal("no pricing id")
	}
	sim := call(creator, data.ID, api.simulatePricing, map[string]any{"distanceMeters": 1000, "durationSeconds": 300, "pickupLatitude": y + 0.015, "pickupLongitude": x + 0.015}, 200)
	var result struct {
		Fares []struct {
			Fare float64 `json:"fareKobo"`
		} `json:"fares"`
	}
	_ = json.Unmarshal(sim["data"], &result)
	if len(result.Fares) != 1 || result.Fares[0].Fare != 750 {
		t.Fatalf("simulation: %s", sim["data"])
	}
	// Wrong editor and stale revisions cannot mutate a draft.
	edit := map[string]any{"revision": 0, "rules": []zonepricing.Rule{{ZoneID: zone, Kind: "fixed", Value: 200}}}
	call(approver, data.ID, api.savePricingZones, edit, 409)
	call(creator, data.ID, api.savePricingZones, edit, 200)
	call(creator, data.ID, api.savePricingZones, edit, 409)
	// Duplicate/overlapping rules fail atomically and retain existing config.
	call(creator, data.ID, api.savePricingZones, map[string]any{"revision": 1, "rules": []zonepricing.Rule{{ZoneID: zone, Kind: "fixed", Value: 200}, {ZoneID: zone, Kind: "fixed", Value: 200}}}, 400)
	call(creator, data.ID, api.approvePricing, nil, 409)
	call(approver, data.ID, api.approvePricing, nil, 200)
	call(creator, data.ID, api.savePricingZones, map[string]any{"revision": 1, "rules": []zonepricing.Rule{}}, 409)
	quote, e := triprepo.NewPostgresRepository(pool).PricingForQuote(ctx, y+0.015, x+0.015)
	if e != nil {
		t.Fatal(e)
	}
	amount, ok := zonepricing.Apply(600, quote.Zone)
	if !ok || amount != 800 {
		t.Fatalf("quote mismatch %v %v", amount, ok)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM pricing_zone_rules WHERE pricing_id=$1::uuid`, data.ID); err == nil {
		t.Fatal("published zone snapshot could be deleted")
	}
	if _, err = pool.Exec(ctx, `UPDATE pricing_zone_rules SET value=900 WHERE pricing_id=$1::uuid`, data.ID); err == nil {
		t.Fatal("published zone snapshot could be edited")
	}
	rider := uuid.NewString()
	exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::uuid,$2,$2,ARRAY['rider'])`, rider, "zone-test:"+rider)
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM ride_fares WHERE rider_id=$1::uuid`, rider)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, rider)
	}()
	route := &triptypes.OSRMApiResponse{}
	route.Routes = append(route.Routes, geo.Route{Distance: 1000, Duration: 300})
	fares, e := tripservice.NewService(triprepo.NewPostgresRepository(pool)).GenerateTripFares(ctx, rider, route, &sharedtypes.Coordinate{Latitude: y + 0.015, Longitude: x + 0.015}, &sharedtypes.Coordinate{Latitude: y + 0.05, Longitude: x + 0.05})
	if e != nil || len(fares) != 1 || fares[0].TotalFareInPaise != 800 {
		t.Fatalf("real quote: %v %v", fares, e)
	}
	sim = call(creator, data.ID, api.simulatePricing, map[string]any{"distanceMeters": 1000, "durationSeconds": 300, "pickupLatitude": y + 0.015, "pickupLongitude": x + 0.015}, 200)
	_ = json.Unmarshal(sim["data"], &result)
	if result.Fares[0].Fare != fares[0].TotalFareInPaise {
		t.Fatal("Admin simulation and persisted Rider quote differ")
	}
	outside, e := zonepricing.Match(ctx, pool, data.ID, y+0.05, x+0.05)
	if e != nil || outside != nil {
		t.Fatal("outside zone must not charge")
	}
}
