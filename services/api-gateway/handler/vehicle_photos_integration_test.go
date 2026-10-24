package handler

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/storage"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

type vehicleEvidenceStore struct{ *privacyMemoryStore }

func (s *vehicleEvidenceStore) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return storage.ObjectInfo{Size: int64(len(s.objects[key])), ContentType: "image/png"}, nil
}
func TestVehicleFindingsAndVideoDuration(t *testing.T) {
	cc := 0
	doors := 0
	for _, f := range []VehicleFindings{{EngineCapacityCC: &cc}, {Doors: &doors}, {FuelType: "rocket"}, {Transmission: "unknownish"}, {Checks: map[string]string{"brakes": "yes"}}, {Checks: map[string]string{"invented": "pass"}}} {
		if f.validate() == nil {
			t.Fatalf("invalid findings accepted: %+v", f)
		}
	}
	if (VehicleFindings{}).validate() != nil {
		t.Fatal("unknown findings must remain recordable")
	}
	if (VehicleFindings{Checks: map[string]string{"brakes": "not_assessed"}}).validate() != nil {
		t.Fatal("not assessed must not imply pass")
	}
	for _, seconds := range []uint32{30, 60, 90, 120, 180} {
		atom := func(kind string, data []byte) []byte {
			b := make([]byte, 8)
			binary.BigEndian.PutUint32(b[:4], uint32(8+len(data)))
			copy(b[4:], kind)
			return append(b, data...)
		}
		header := make([]byte, 20)
		binary.BigEndian.PutUint32(header[12:16], 1000)
		binary.BigEndian.PutUint32(header[16:20], seconds*1000)
		handler := make([]byte, 12)
		copy(handler[8:], "vide")
		data := append(atom("moov", append(atom("mvhd", header), atom("trak", atom("mdia", atom("hdlr", handler)))...)), atom("mdat", []byte{1})...)

		duration, err := vehicleVideoDuration(bytes.NewReader(data), int64(len(data)))
		if err != nil || duration != float64(seconds) {
			t.Fatalf("duration=%v err=%v", duration, err)
		}
	}
	if _, err := vehicleVideoDuration(bytes.NewReader([]byte("bad video")), 9); err == nil {
		t.Fatal("invalid video accepted")
	}
}
func TestVehicleEvidenceOwnershipSnapshotsAndPrimaryPhoto(t *testing.T) {
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
	owner, other, admin := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO admin_users(id,username,password_hash) VALUES($1::uuid,$1,'test')`, admin)
	for _, id := range []string{owner, other} {
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::uuid,$1,$1,ARRAY['driver'])`, id)
		exec(`INSERT INTO driver_profiles(driver_id) VALUES($1::uuid)`, id)
		exec(`INSERT INTO driver_vehicles(driver_id,make,model,plate,review_status,inspection_revision) VALUES($1::uuid,'Toyota','Corolla','QA','approved',1)`, id)
	}
	store := &vehicleEvidenceStore{newPrivacyMemoryStore()}
	api := &adminAPI{pool: pool, files: store}
	driver := &driverAPI{pool: pool, files: store}
	call := func(actor string, h gin.HandlerFunc, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := callAdminHandler(t, admin, func(c *gin.Context) {
			c.Set("authenticated-user", gatewayauth.User{ID: actor, Roles: []string{"driver"}})
			h(c)
		}, raw)
		if r.Code != want {
			t.Fatalf("want=%d got=%d %s", want, r.Code, r.Body.String())
		}
		return r
	}
	buffer := new(bytes.Buffer)
	_ = png.Encode(buffer, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	data := buffer.Bytes()
	upload := func(actor, angle string, want int) string {
		t.Helper()
		r := call(actor, driver.reserveOwnVehiclePhoto, map[string]any{"angle": angle, "vehicleRevision": 1, "contentType": "image/png", "sizeBytes": len(data)}, 200)
		var v struct {
			Data struct {
				ID string `json:"uploadId"`
			}
		}
		_ = json.Unmarshal(r.Body.Bytes(), &v)
		var key string
		if err := pool.QueryRow(ctx, `SELECT storage_key FROM vehicle_photo_uploads WHERE id=$1::uuid`, v.Data.ID).Scan(&key); err != nil {
			t.Fatal(err)
		}
		store.objects[key] = data
		call(actor, driver.submitOwnVehiclePhoto, map[string]any{"uploadId": v.Data.ID}, want)
		return v.Data.ID
	}
	id := upload(owner, "front", 200)
	call(other, driver.submitOwnVehiclePhoto, map[string]any{"uploadId": id}, 409)
	call(owner, driver.submitOwnVehiclePhoto, map[string]any{"uploadId": id}, 409)
	// Retake before submission preserves history but only returns the latest photo.
	upload(owner, "front", 200)
	r := call(owner, driver.ownVehiclePhotos, nil, 200)
	if bytes.Count(r.Body.Bytes(), []byte(`"angle":"front"`)) != 1 {
		t.Fatal("superseded photos leaked")
	}
	call(owner, driver.vehicle, nil, 200)
	if store.presignTTL != 2*time.Minute {
		t.Fatal("private preview expiry changed")
	}
	var mobile struct {
		Data struct {
			Photos []any `json:"photos"`
		}
	}
	_ = json.Unmarshal(call(owner, driver.vehicle, nil, 200).Body.Bytes(), &mobile)
	if len(mobile.Data.Photos) != 0 {
		t.Fatal("unapproved inspection image exposed")
	}
	checks := map[string]string{}
	for _, key := range vehicleConditionChecks {
		checks[key] = "pass"
	}
	snapshot, _ := json.Marshal(map[string]any{"findings": VehicleFindings{Checks: checks}})
	inspection := uuid.NewString()
	exec(`INSERT INTO driver_inspections(id,driver_id,report_storage_key,status,vehicle_revision,evidence_version,vehicle_snapshot) VALUES($1::uuid,$2::uuid,'report','report_received',1,1,$3::jsonb)`, inspection, owner, snapshot)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	tx, _ := pool.Begin(ctx)
	if validateInspectionEvidence(c, tx, owner, inspection) == nil {
		t.Fatal("missing evidence approved")
	}
	tx.Rollback(ctx)
	// Photos are frozen by submission, including for their original uploader.
	upload(owner, "rear", 409)
	for _, angle := range vehiclePhotoAngles {
		if angle == "front" {
			continue
		}
		exec(`INSERT INTO driver_vehicle_photos(id,driver_id,vehicle_revision,angle,storage_key,content_type,size_bytes,uploaded_by,uploader_type) VALUES($1::uuid,$2::uuid,1,$3,$4,'image/png',10,$2::uuid,'driver')`, uuid.NewString(), owner, angle, "fixture/"+uuid.NewString())
	}
	tx, _ = pool.Begin(ctx)
	if err = validateInspectionEvidence(c, tx, owner, inspection); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(ctx)
	failedChecks := map[string]string{}
	for _, key := range vehicleConditionChecks {
		failedChecks[key] = "pass"
	}
	failedChecks["brakes"] = "fail"
	failedSnapshot, _ := json.Marshal(map[string]any{"findings": VehicleFindings{Checks: failedChecks}})
	exec(`UPDATE driver_inspections SET vehicle_snapshot=$2::jsonb WHERE id=$1::uuid`, inspection, failedSnapshot)
	tx, _ = pool.Begin(ctx)
	if validateInspectionEvidence(c, tx, owner, inspection) == nil {
		t.Fatal("failed brakes approved despite complete media")
	}
	tx.Rollback(ctx)
	failedChecks["brakes"] = "not_assessed"
	failedSnapshot, _ = json.Marshal(map[string]any{"findings": VehicleFindings{Checks: failedChecks}})
	exec(`UPDATE driver_inspections SET vehicle_snapshot=$2::jsonb WHERE id=$1::uuid`, inspection, failedSnapshot)
	tx, _ = pool.Begin(ctx)
	if validateInspectionEvidence(c, tx, owner, inspection) == nil {
		t.Fatal("unassessed brakes approved")
	}
	tx.Rollback(ctx)
	exec(`UPDATE driver_inspections SET vehicle_snapshot=$2::jsonb WHERE id=$1::uuid`, inspection, snapshot)
	exec(`UPDATE driver_inspections SET status='completed' WHERE id=$1::uuid`, inspection)
	_ = json.Unmarshal(call(owner, driver.vehicle, nil, 200).Body.Bytes(), &mobile)
	if len(mobile.Data.Photos) != 1 {
		t.Fatal("primary front photo missing, or VIN/video leaked into profile")
	}
	exec(`UPDATE driver_vehicles SET inspection_revision=2 WHERE driver_id=$1::uuid`, owner)
	_ = json.Unmarshal(call(owner, driver.vehicle, nil, 200).Body.Bytes(), &mobile)
	if len(mobile.Data.Photos) != 0 {
		t.Fatal("old vehicle photo displayed after car changed")
	}
	_ = api
}
