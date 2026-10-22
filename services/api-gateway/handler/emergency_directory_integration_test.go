package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"

	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestEmergencyDirectoryPersistence(t *testing.T) {
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
	if _, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$2,'test')`, staff, "support-"+staff); err != nil {
		t.Fatal(err)
	}
	var backup []byte
	_ = pool.QueryRow(ctx, `SELECT row_to_json(s)::TEXT FROM admin_emergency_directory s WHERE id=1`).Scan(&backup)
	defer func() {
		pool.Exec(ctx, `DELETE FROM admin_emergency_directory WHERE id=1`)
		if len(backup) > 0 {
			pool.Exec(ctx, `INSERT INTO admin_emergency_directory SELECT * FROM json_populate_record(NULL::admin_emergency_directory,$1::JSON)`, string(backup))
		}
	}()
	pool.Exec(ctx, `DELETE FROM admin_emergency_directory WHERE id=1`)
	api := &adminAPI{pool: pool}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("admin", adminIdentity{ID: staff, Role: "super_admin"}); c.Next() })
	router.GET("/geo", api.getEmergencyDirectory)
	router.PUT("/geo", api.putEmergencyDirectory)
	request := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/geo", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		return w
	}
	body, _ := json.Marshal(map[string]any{"contacts": []map[string]string{{"name": "State emergency", "phone": "112"}}, "version": 0, "reason": "Emergency directory setup"})
	if w := request(string(body)); w.Code != 200 {
		t.Fatalf("save failed: %d %s", w.Code, w.Body.String())
	}
	var raw []byte
	var version int64
	if err = pool.QueryRow(ctx, `SELECT contacts,version FROM admin_emergency_directory WHERE id=1`).Scan(&raw, &version); err != nil || !strings.Contains(string(raw), "112") || version != 1 {
		t.Fatalf("not persisted: %s %d %v", string(raw), version, err)
	}
	if w := request(string(body)); w.Code != 409 {
		t.Fatal("stale update accepted")
	}
	var audits int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM admin_audit WHERE admin_id=$1::UUID AND action='emergency.directory.updated'`, staff).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit not persisted: %v %d", err, audits)
	}
	if adminRoutePermission(http.MethodPut, "/admin/settings/emergency-contacts") != permissionSettings || adminRoutePermission(http.MethodGet, "/admin/settings/emergency-contacts") != permissionSettings {
		t.Fatal("settings permission missing")
	}
}
