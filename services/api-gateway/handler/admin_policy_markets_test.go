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

func TestPolicyMarketsPermission(t *testing.T) {
	if adminRoutePermission(http.MethodGet, "/admin/pricing/markets") != permissionFinanceRead {
		t.Fatal("Market identities must require finance.read")
	}
	if adminRoutePermission(http.MethodGet, "/admin/geofences") != permissionGeofenceRead {
		t.Fatal("Boundary permission was broadened")
	}
}
func TestHeroesPolicyMarketsPublishedIdentityOnly(t *testing.T) {
	dsn := os.Getenv("HEROES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("Disposable local database required")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	creator, approver := uuid.NewString(), uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test'),($2::uuid,$2,'test')`, creator, approver); err != nil {
		t.Fatal(err)
	}
	code := "POLICY_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	for i, kind := range []string{"market", "market", "market", "region"} {
		status := "approved"
		from := time.Now().Add(-time.Hour)
		var until *time.Time
		var approvedBy *string
		var approvedAt *time.Time
		if i == 1 {
			from = time.Now().Add(time.Hour)
		}
		if i == 2 {
			status = "draft"
		}
		if status == "approved" {
			approvedBy = &approver
			at := time.Now()
			approvedAt = &at
		}
		_, err = pool.Exec(ctx, `INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,effective_until,created_by,approved_by,approved_at)VALUES($1::uuid,$2,$3,'Published market',1,ST_Multi(ST_GeomFromText('POLYGON((3 6,4 6,4 7,3 7,3 6))',4326)),$4,$5,$6,$7::uuid,$8::uuid,$9)`, uuid.NewString(), kind, code+string(rune('A'+i)), status, from, until, creator, approvedBy, approvedAt)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := gin.New()
	a := &adminAPI{pool: pool}
	r.GET("/markets", a.policyMarkets)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/markets", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range response.Data {
		if strings.HasPrefix(row["code"].(string), code) {
			if row["code"] != code+"A" {
				t.Fatal("Unpublished or region identity leaked", row)
			}
			found = true
		}
		if len(row) != 2 || row["name"] == nil || row["code"] == nil {
			t.Fatal("Only market identity may be exposed", row)
		}
	}
	if !found {
		t.Fatal("Published market missing")
	}
}
