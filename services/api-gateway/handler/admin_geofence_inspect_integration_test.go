package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGeofencePointInspectorSpatialAndEffectiveRules(t *testing.T) {
	url := os.Getenv("GEOFENCE_INSPECTOR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GEOFENCE_INSPECTOR_TEST_DATABASE_URL requires a disposable test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pool, e := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 3})
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = shareddb.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	actor, approver, seedID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seed, _ := strconv.ParseUint(seedID[:4], 16, 16)
	x, y := 100+float64(seed)/10000, 20+float64(seed)/100000
	code := "INSP_" + strings.ToUpper(seedID[:8])
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO admin_users(id,username,password_hash) VALUES($1::uuid,$1,'test'),($2::uuid,$2,'test')`, actor, approver)
	ring := func(a, b, c, d float64) string {
		return fmt.Sprintf("(%f %f,%f %f,%f %f,%f %f,%f %f)", a, b, c, b, c, d, a, d, a, b)
	}
	market := "MULTIPOLYGON((" + ring(x, y, x+.1, y+.1) + "))"
	region := "MULTIPOLYGON((" + ring(x+.01, y+.01, x+.02, y+.02) + "," + ring(x+.014, y+.014, x+.016, y+.016) + "))"
	airport := "MULTIPOLYGON((" + ring(x+.011, y+.011, x+.012, y+.012) + "),(" + ring(x+.04, y+.04, x+.045, y+.045) + "))"
	definitions := []struct {
		kind, name, boundary, status string
		from, until                  time.Time
	}{
		{"market", "MARKET", market, "approved", time.Now().Add(-48 * time.Hour), time.Time{}},
		{"region", "REGION", region, "approved", time.Now().Add(-48 * time.Hour), time.Time{}},
		{"region", "ADJACENT", "MULTIPOLYGON((" + ring(x+.02, y+.01, x+.03, y+.02) + "))", "approved", time.Now().Add(-48 * time.Hour), time.Time{}},
		{"airport", "AIRPORT", airport, "approved", time.Now().Add(-48 * time.Hour), time.Time{}},
		{"region", "DRAFT", region, "draft", time.Now().Add(-48 * time.Hour), time.Time{}},
		{"region", "EXPIRED", region, "approved", time.Now().Add(-48 * time.Hour), time.Now().Add(-24 * time.Hour)},
		{"region", "FUTURE", region, "approved", time.Now().Add(24 * time.Hour), time.Time{}},
	}
	ids := []string{}
	for _, d := range definitions {
		id := uuid.NewString()
		ids = append(ids, id)
		var until any
		if !d.until.IsZero() {
			until = d.until
		}
		var by any
		var at any
		if d.status == "approved" {
			by = approver
			at = time.Now()
		}
		exec(`INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,effective_until,created_by,approved_by,approved_at) VALUES($1::uuid,$2,$3,$3,1,ST_GeomFromText($4,4326),$5,$6,$7,$8::uuid,$9::uuid,$10)`, id, d.kind, code+"_"+d.name, d.boundary, d.status, d.from, until, actor, by, at)
	}
	defer func() {
		for i, id := range ids {
			d := definitions[i]
			if !d.until.IsZero() {
				continue
			}
			end := time.Now().Add(time.Second)
			if end.Before(d.from) {
				end = d.from.Add(time.Hour)
			}
			if _, err := pool.Exec(context.Background(), `UPDATE trip_geofences SET effective_until=$2 WHERE id=$1::uuid AND effective_until IS NULL`, id, end); err != nil {
				t.Error(err)
			}
		}
	}()
	api := &adminAPI{pool: pool}
	inspect := func(lng, lat float64) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", fmt.Sprintf("/admin/geofences/inspect?latitude=%.8f&longitude=%.8f", lat, lng), nil)
		api.inspectGeofencePoint(c)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var response struct {
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &response) != nil {
			t.Fatal("invalid response")
		}
		return response.Data
	}
	inside := inspect(x+.0115, y+.0115)
	classification := inside["classification"].(map[string]any)
	if classification["market"] != code+"_MARKET" || classification["region"] != code+"_REGION" || classification["airport"] != code+"_AIRPORT" || classification["complete"] != true {
		t.Fatal(classification)
	}
	if len(inside["matches"].([]any)) != 6 {
		t.Fatalf("expected actual and historical containing polygons: %v", inside["matches"])
	}
	hole := inspect(x+.015, y+.015)["classification"].(map[string]any)
	if hole["region"] != nil || hole["complete"] != false {
		t.Fatal("hole classified as covered", hole)
	}
	otherPart := inspect(x+.042, y+.042)["classification"].(map[string]any)
	if otherPart["airport"] != code+"_AIRPORT" {
		t.Fatal("multipolygon part missed")
	}
	boundary := inspect(x+.02, y+.0115)
	bc := boundary["classification"].(map[string]any)
	if len(bc["ambiguousKinds"].([]any)) != 1 || bc["region"] != nil {
		t.Fatal("boundary ambiguity hidden", bc)
	}
	outside := inspect(x+1, y+1)
	if len(outside["matches"].([]any)) != 0 {
		t.Fatal("outside point matched")
	}
	if outside["pricing"].(map[string]any)["status"] != "unavailable" {
		t.Fatal("fabricated pricing")
	}
}
func TestGeofenceInspectorRejectsInvalidCoordinates(t *testing.T) {
	api := &adminAPI{}
	for _, query := range []string{"", "latitude=NaN&longitude=3", "latitude=6&longitude=Inf", "latitude=91&longitude=3", "latitude=6&longitude=181"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/admin/geofences/inspect?"+query, nil)
		api.inspectGeofencePoint(c)
		if w.Code != 400 {
			t.Fatalf("invalid coordinate accepted: %s", query)
		}
	}
}
