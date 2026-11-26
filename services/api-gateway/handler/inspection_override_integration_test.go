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

// Seven pictures finish the evidence; the walkaround video is no longer needed.
// An admin can then pass the inspection with no report, but only with a reason
// and only in the right role.
func TestInspectionOverrideAndPicturesOnlyEvidence(t *testing.T) {
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
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	driver, admin := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO admin_users(id,username,password_hash) VALUES($1::uuid,$1,'test')`, admin)
	exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::uuid,$1,$1,ARRAY['driver'])`, driver)
	exec(`INSERT INTO driver_profiles(driver_id) VALUES($1::uuid)`, driver)
	exec(`INSERT INTO driver_vehicles(driver_id,make,model,plate,review_status,inspection_revision) VALUES($1::uuid,'Toyota','Corolla','QA','approved',1)`, driver)

	// Six pictures are not enough; the seventh opens review with no video.
	for _, angle := range requiredVehiclePhotoAngles[:6] {
		exec(`INSERT INTO driver_vehicle_photos(id,driver_id,vehicle_revision,angle,storage_key,content_type,size_bytes,uploaded_by,uploader_type) VALUES($1::uuid,$2::uuid,1,$3,$4,'image/png',10,$2::uuid,'driver')`, uuid.NewString(), driver, angle, "fixture/"+uuid.NewString())
	}
	complete := func() {
		t.Helper()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = markVehicleEvidenceComplete(c, tx, driver, 1); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	count := func() (n int) {
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_inspections WHERE driver_id=$1::uuid`, driver).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	complete()
	if count() != 0 {
		t.Fatal("six pictures opened review")
	}
	exec(`INSERT INTO driver_vehicle_photos(id,driver_id,vehicle_revision,angle,storage_key,content_type,size_bytes,uploaded_by,uploader_type) VALUES($1::uuid,$2::uuid,1,'vin',$3,'image/png',10,$2::uuid,'driver')`, uuid.NewString(), driver, "fixture/"+uuid.NewString())
	complete()
	if count() != 1 {
		t.Fatalf("seven pictures should open review, got %d inspections", count())
	}
	var inspection, status string
	if err = pool.QueryRow(ctx, `SELECT id::text,status FROM driver_inspections WHERE driver_id=$1::uuid`, driver).Scan(&inspection, &status); err != nil || status != "report_received" {
		t.Fatalf("inspection %q status %q err %v", inspection, status, err)
	}

	api := &adminAPI{pool: pool}
	review := func(role string, body map[string]any, want int) {
		t.Helper()
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(http.MethodPost, "/admin/test", bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = request
		c.Params = gin.Params{{Key: "driverID", Value: driver}, {Key: "inspectionID", Value: inspection}}
		c.Set("admin", adminIdentity{ID: admin, Username: "staff", Role: role})
		api.reviewInspection(c)
		if recorder.Code != want {
			t.Fatalf("%s %v: want %d got %d %s", role, body, want, recorder.Code, recorder.Body.String())
		}
	}
	// Without the override a pictures-only inspection still cannot be approved.
	review("compliance", map[string]any{"status": "completed", "reason": "ok"}, http.StatusConflict)
	review("support", map[string]any{"status": "completed", "reason": "ok", "override": true}, http.StatusForbidden)
	review("compliance", map[string]any{"status": "completed", "reason": "  ", "override": true}, http.StatusBadRequest)
	review("compliance", map[string]any{"status": "failed", "reason": "no", "override": true}, http.StatusBadRequest)
	review("compliance", map[string]any{"status": "completed", "reason": "Vehicle licence and pictures checked", "override": true}, http.StatusOK)

	if err = pool.QueryRow(ctx, `SELECT status FROM driver_inspections WHERE id=$1::uuid`, inspection).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("status after override = %q err %v", status, err)
	}
	var audited bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_audit WHERE action='inspection.review' AND details->>'override'='true' AND details->>'inspectionId'=$1)`, inspection).Scan(&audited); err != nil || !audited {
		t.Fatalf("override not audited: %v %v", audited, err)
	}
}
