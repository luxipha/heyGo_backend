package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestSaveDriverRequirementOnRealSchema(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
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
	admin := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::uuid,$1,'test')`, admin); err != nil {
		t.Fatal(err)
	}
	api := &adminAPI{pool: pool}
	save := func(kind string, body map[string]any, want int) {
		t.Helper()
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(http.MethodPut, "/admin/driver-requirements/"+kind, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = request
		c.Params = gin.Params{{Key: "type", Value: kind}}
		c.Set("admin", adminIdentity{ID: admin, Username: "staff", Role: "super_admin"})
		api.saveDriverRequirement(c)
		if recorder.Code != want {
			t.Fatalf("%s %v: want %d got %d %s", kind, body, want, recorder.Code, recorder.Body.String())
		}
	}
	state := func(kind string) (mode string, since *string) {
		if err := pool.QueryRow(ctx, `SELECT mode,required_since::text FROM driver_requirement_config WHERE type=$1`, kind).Scan(&mode, &since); err != nil {
			t.Fatal(err)
		}
		return
	}
	var mode string
	var since *string
	if mode, _ = state("vehicle_inspection"); mode != "off" {
		t.Fatalf("inspection default = %q, want off", mode)
	}
	save("vehicle_inspection", map[string]any{"mode": "maybe"}, http.StatusBadRequest)
	save("identity", map[string]any{"mode": "off"}, http.StatusBadRequest)
	save("vehicle_inspection", map[string]any{"mode": "required", "graceDays": 400}, http.StatusBadRequest)
	save("vehicle_inspection", map[string]any{"mode": "required", "graceDays": 14}, http.StatusOK)
	if mode, since = state("vehicle_inspection"); mode != "required" || since == nil {
		t.Fatalf("after Required: mode=%q since=%v", mode, since)
	}
	save("vehicle_inspection", map[string]any{"mode": "optional"}, http.StatusOK)
	if mode, since = state("vehicle_inspection"); mode != "optional" || since != nil {
		t.Fatalf("after Optional: mode=%q since=%v (the grace window should clear)", mode, since)
	}
	var audits int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM admin_audit WHERE action='driver.requirement.config'`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit rows = %d err %v, want 2", audits, err)
	}
	save("vehicle_inspection", map[string]any{"mode": "off"}, http.StatusOK)
}
