package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAdminGeoSettingsPersistence(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	staff := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'test')`, staff, "geo-"+staff); err != nil {
		t.Fatal(err)
	}
	var backup []byte
	_ = pool.QueryRow(ctx, `SELECT row_to_json(s)::TEXT FROM admin_geo_settings s WHERE id=1`).Scan(&backup)
	defer func() {
		pool.Exec(ctx, `DELETE FROM admin_geo_settings WHERE id=1`)
		if len(backup) > 0 {
			pool.Exec(ctx, `INSERT INTO admin_geo_settings SELECT * FROM json_populate_record(NULL::admin_geo_settings,$1::JSON)`, string(backup))
		}
	}()
	pool.Exec(ctx, `DELETE FROM admin_geo_settings WHERE id=1`)
	api := &adminAPI{pool: pool}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("admin", adminIdentity{ID: staff, Role: "super_admin"}); c.Next() })
	router.GET("/geo", api.getGeoSettings)
	router.PUT("/geo", api.putGeoSettings)
	request := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/geo", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		return w
	}
	settings := geo.Settings{RouteProvider: "osrm", OSRMEndpoint: "http://router.project-osrm.org/route/v1/driving", AllowDemo: true, MapProvider: "google", PlaceProvider: "google", NavigationProvider: "google"}
	body, _ := json.Marshal(map[string]any{"settings": settings, "version": 0, "reason": "Testing week demo endpoint"})
	if w := request(string(body)); w.Code != 200 {
		t.Fatalf("save failed %d %s", w.Code, w.Body.String())
	}
	saved, err := geo.ReadSettings(ctx, pool)
	if err != nil || saved.Version != 1 || !saved.Settings.AllowDemo || saved.Source != "admin" {
		t.Fatalf("not persisted: %+v %v", saved, err)
	}
	if w := request(string(body)); w.Code != 409 {
		t.Fatal("stale update overwrote settings")
	}
	settings.OSRMEndpoint = "https://127.0.0.1/route/v1/driving"
	body, _ = json.Marshal(map[string]any{"settings": settings, "version": 1, "reason": "invalid target"})
	if w := request(string(body)); w.Code != 400 {
		t.Fatal("private endpoint accepted")
	}
	var audits int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM admin_audit WHERE admin_id=$1::UUID AND action='geo.settings.updated'`, staff).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit not persisted: %v %d", err, audits)
	}
	if adminRoutePermission(http.MethodPut, "/admin/settings/geo") != permissionSettings || adminRoutePermission(http.MethodGet, "/admin/settings/geo") != permissionSettings {
		t.Fatal("settings permission missing")
	}
}
