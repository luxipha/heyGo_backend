package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestActivateOfferZoneScope(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	t.Setenv("HEYGO_OFFERS_ENABLED", "true")
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	suffix := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:8]
	market := "OFFERZONE" + suffix
	creator, approver := uuid.NewString(), uuid.NewString()
	for _, id := range []string{creator, approver} {
		exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, id)
	}
	var created []string
	// Approved geofences cannot be deleted, so retire them (and drop drafts) when
	// the test ends; a leftover market polygon would make other tests' pickups
	// ambiguous. A defer, not t.Cleanup: it must run before the pool is closed.
	defer func() {
		_, _ = pool.Exec(context.Background(), `UPDATE trip_geofences SET effective_until=NOW() WHERE id=ANY($1::text[]::uuid[]) AND status='approved' AND effective_until IS NULL`, created)
		_, _ = pool.Exec(context.Background(), `DELETE FROM trip_geofences WHERE id=ANY($1::text[]::uuid[]) AND status='draft'`, created)
	}()
	geofence := func(kind, code, status, wkt string) string {
		id := uuid.NewString()
		created = append(created, id)
		var by, at any
		if status == "approved" {
			by, at = approver, time.Now()
		}
		exec(`INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at)VALUES($1::uuid,$2,$3,$3,1,ST_Multi(ST_GeomFromText($4,4326)),$5,NOW()-INTERVAL '2 days',$6::uuid,$7::uuid,$8)`, id, kind, code, wkt, status, creator, by, at)
		return id
	}
	geofence("market", market, "approved", "POLYGON((3 6,4 6,4 7,3 7,3 6))")
	ikeja := geofence("region", "IKEJA"+suffix, "approved", "POLYGON((3.3 6.3,3.5 6.3,3.5 6.5,3.3 6.5,3.3 6.3))")
	draftZone := geofence("region", "DRAFT"+suffix, "draft", "POLYGON((3.3 6.3,3.5 6.3,3.5 6.5,3.3 6.5,3.3 6.3))")
	outside := geofence("region", "OUTSIDE"+suffix, "approved", "POLYGON((9 9,9.5 9,9.5 9.5,9 9.5,9 9))")

	a := &adminAPI{pool: pool}
	router := gin.New()
	router.POST("/activate/:campaignID", func(c *gin.Context) {
		c.Set("admin", adminIdentity{ID: creator, Username: "offer-zone-test", Role: "admin"})
		a.activateOffer(c)
	})
	activate := func(zones any) (int, map[string]any) {
		t.Helper()
		draft := uuid.NewString()
		exec(`INSERT INTO admin_campaign_drafts(id,kind,name,category,audience,body,budget_kobo,created_by,updated_by)VALUES($1::uuid,'promotion','Ikeja launch','Discount','Riders','Ikeja launch',500000,$2::uuid,$2::uuid)`, draft, creator)
		body := map[string]any{
			"version": 1, "reason": "launch zone offer", "kind": "discount", "discountKobo": 50000, "multiplier": 1,
			"startsAt": time.Now().Add(-time.Hour), "endsAt": time.Now().Add(48 * time.Hour),
			"markets": []string{market}, "categories": []string{"sedan"}, "startHour": 0, "endHour": 24, "timezone": "Africa/Lagos",
			"maxUses": 10, "perRiderLimit": 1,
		}
		if zones != nil {
			body["zoneIds"] = zones
		}
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/activate/"+draft, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code == http.StatusOK {
			var stored []string
			if e := pool.QueryRow(ctx, `SELECT zone_ids::text[] FROM rider_offers WHERE id=$1::uuid`, draft).Scan(&stored); e != nil {
				t.Fatal(e)
			}
			out["stored"] = stored
		}
		return rec.Code, out
	}
	errorCode := func(out map[string]any) string {
		e, _ := out["error"].(map[string]any)
		code, _ := e["code"].(string)
		return code
	}

	if code, out := activate([]string{ikeja}); code != 200 || len(out["stored"].([]string)) != 1 || out["stored"].([]string)[0] != ikeja {
		t.Fatal("approved zone in the market was not published", code, out)
	}
	var audited int
	if e := pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit WHERE admin_id=$1::uuid AND action='promotion.activated' AND target_type='campaign' AND details->'zoneIds' @> to_jsonb($2::text)`, creator, ikeja).Scan(&audited); e != nil || audited != 1 {
		t.Fatal("activation was not audited against the campaign with its zones", audited, e)
	}
	if code, out := activate(nil); code != 200 || len(out["stored"].([]string)) != 0 {
		t.Fatal("an offer with no zones must still publish market-wide", code, out)
	}
	if code, out := activate([]string{draftZone}); code != 400 || errorCode(out) != "invalid_zone" {
		t.Fatal("draft zone accepted", code, out)
	}
	if code, out := activate([]string{outside}); code != 400 || errorCode(out) != "invalid_zone" {
		t.Fatal("zone outside the market accepted", code, out)
	}
	if code, out := activate([]string{uuid.NewString()}); code != 400 || errorCode(out) != "invalid_zone" {
		t.Fatal("unknown zone accepted", code, out)
	}
	if code, out := activate([]string{ikeja, ikeja}); code != 400 || errorCode(out) != "invalid_offer" {
		t.Fatal("duplicate zone accepted", code, out)
	}
	if code, out := activate([]string{"ikeja"}); code != 400 || errorCode(out) != "invalid_offer" {
		t.Fatal("non-uuid zone accepted", code, out)
	}
}
