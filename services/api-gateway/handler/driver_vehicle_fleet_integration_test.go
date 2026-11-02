package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"net/http/httptest"
	"os"
	"testing"
)

func TestDriverFleetIsolationSwitchingAndSnapshots(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	p, e := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 8})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = shareddb.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := p.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	owner, other, admin, rider := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, other, rider} {
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles,email_verified)VALUES($1::uuid,$1,$1,ARRAY['driver','rider'],TRUE)`, id)
	}
	// Fleet switching now requires a verified contact; this fixture never sends SMS.
	exec(`UPDATE users SET phone_e164='+999'||lpad((floor(random()*999999999999)::bigint)::text,12,'0'),phone_verified_at=NOW() WHERE id=$1::uuid`, owner)
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'!test')`, admin)
	exec(`INSERT INTO driver_profiles(driver_id,admin_status,manual_verification_status,admin_reviewed_at)VALUES($1::uuid,'approved','approved',NOW()+INTERVAL '1 minute')`, owner)
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status,online_requested)VALUES($1::uuid,'Fleet QA','CAR-A','sedan','offline',FALSE)`, owner)
	a, b := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO driver_vehicle_records(id,driver_id,make,model,plate,package_slug,review_status,approval_status,approved_at)VALUES($1::uuid,$2::uuid,'Toyota','Corolla','CAR-A','sedan','approved','approved',NOW()+INTERVAL '1 minute')`, a, owner)
	seedEvidence := func(car string) {
		t.Helper()
		tx, e := p.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		_, e = tx.Exec(ctx, `SELECT set_config('heygo.vehicle_id',$1,TRUE)`, car)
		if e != nil {
			t.Fatal(e)
		}
		for _, kind := range []string{"vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"} {
			_, e = tx.Exec(ctx, `INSERT INTO driver_documents(id,driver_id,type,status,storage_key)VALUES(gen_random_uuid(),$1::uuid,$2,'approved','synthetic')`, owner, kind)
			if e != nil {
				t.Fatal(e)
			}
		}
		_, e = tx.Exec(ctx, `INSERT INTO driver_inspections(id,driver_id,status,report_storage_key,vehicle_revision)VALUES(gen_random_uuid(),$1::uuid,'completed','synthetic',0)`, owner)
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	seedEvidence(a)
	exec(`INSERT INTO driver_documents(id,driver_id,type,status,storage_key)VALUES(gen_random_uuid(),$1::uuid,'driver_license','approved','synthetic')`, owner)
	eligible := func() bool {
		t.Helper()
		var v bool
		if e := p.QueryRow(ctx, `SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=$1::uuid`, owner).Scan(&v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	if !eligible() {
		t.Fatal("initial approved car not eligible")
	}
	api := &driverAPI{pool: p, files: &vehicleEvidenceStore{newPrivacyMemoryStore()}}
	staff := &adminAPI{pool: p}
	call := func(actor, id, method, scope string, h gin.HandlerFunc, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(r)
		c.Request = httptest.NewRequest(method, "/test"+scope, bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("authenticated-user", gatewayauth.User{ID: actor, Roles: []string{"driver"}})
		c.Set("admin", adminIdentity{ID: admin})
		c.Params = gin.Params{{Key: "vehicleID", Value: id}}
		if actor == admin {
			c.Params = append(c.Params, gin.Param{Key: "driverID", Value: owner})
		}
		if scope == "" && (method == "PUT" || method == "POST") {
			h(c)
		} else {
			withVehicleScope(p, h)(c)
		}
		if r.Code != want {
			t.Fatalf("want %d got %d: %s", want, r.Code, r.Body)
		}
		return r
	}
	call(owner, "", "POST", "", api.addVehicle, map[string]any{"make": "Toyota", "model": "RAV4", "year": 2022, "plate": "CAR-B"}, 201)
	if !eligible() {
		t.Fatal("adding pending car disabled approved active car")
	}
	if e = p.QueryRow(ctx, `SELECT id::text FROM driver_vehicle_records WHERE driver_id=$1::uuid AND NOT is_active`, owner).Scan(&b); e != nil {
		t.Fatal(e)
	}
	photoA, photoB, reservation := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO driver_vehicle_photos_records(id,driver_id,vehicle_id,vehicle_revision,angle,storage_key,content_type,size_bytes,uploaded_by,uploader_type)VALUES($1::uuid,$2::uuid,$3::uuid,0,'front','car-a','image/png',1,$2::uuid,'driver'),($4::uuid,$2::uuid,$5::uuid,0,'front','car-b','image/png',1,$2::uuid,'driver')`, photoA, owner, a, photoB, b)
	media := call(owner, b, "GET", "?vehicleId="+b, api.ownVehiclePhotos, nil, 200)
	if bytes.Contains(media.Body.Bytes(), []byte(photoA)) || !bytes.Contains(media.Body.Bytes(), []byte(photoB)) {
		t.Fatal("photos leaked between cars at the same revision")
	}
	exec(`INSERT INTO vehicle_photo_uploads_records(id,driver_id,vehicle_id,actor_id,actor_type,vehicle_revision,angle,storage_key,content_type,size_bytes,expires_at)VALUES($1::uuid,$2::uuid,$3::uuid,$2::uuid,'driver',0,'rear','car-a-reservation-'||$1::text,'image/png',1,NOW()+INTERVAL '10 minutes')`, reservation, owner, a)
	call(owner, b, "POST", "?vehicleId="+b, api.submitOwnVehiclePhoto, map[string]any{"uploadId": reservation}, 409)
	call(owner, b, "PUT", "", api.activateVehicle, nil, 409)
	call(other, b, "GET", "?vehicleId="+b, api.vehicle, nil, 404)
	// A's inspection and documents must not appear on B, despite identical revisions.
	response := call(owner, b, "GET", "?vehicleId="+b, api.vehicle, nil, 200)
	if !bytes.Contains(response.Body.Bytes(), []byte(`"inspectionStatus":"required"`)) {
		t.Fatal("inspection leaked across vehicles")
	}
	call(owner, b, "PATCH", "?vehicleId="+b, api.updateVehicle, map[string]any{"make": "Toyota", "model": "RAV4", "year": 2022, "plate": "CAR-B"}, 200)
	if !eligible() {
		t.Fatal("editing inactive car invalidated account")
	}
	// Restore B revision to make synthetic fixture independent from form review implementation.
	exec(`UPDATE driver_vehicle_records SET inspection_revision=0,review_status='approved',package_slug='suv' WHERE id=$1::uuid`, b)
	seedEvidence(b)
	exec(`UPDATE driver_profiles SET admin_status='pending' WHERE driver_id=$1::uuid`, owner)
	call(admin, b, "POST", "?vehicleId="+b, staff.reviewDriver, map[string]any{"status": "approved", "reason": "Synthetic test approval"}, 200)
	if !eligible() {
		t.Fatal("approving B affected active A")
	}
	// An expired inactive car cannot be selected, and expiry does not affect A.
	exec(`UPDATE driver_documents_records SET expires_at=NOW()-INTERVAL '1 second' WHERE vehicle_id=$1::uuid AND type='auto_insurance'`, b)
	call(owner, b, "PUT", "", api.activateVehicle, nil, 409)
	if !eligible() {
		t.Fatal("inactive expired evidence affected active car")
	}
	exec(`UPDATE driver_documents_records SET expires_at=NULL WHERE vehicle_id=$1::uuid AND type='auto_insurance'`, b)
	exec(`UPDATE drivers SET online_requested=TRUE WHERE id=$1::uuid`, owner)
	call(owner, b, "PUT", "", api.activateVehicle, nil, 409)
	exec(`UPDATE drivers SET online_requested=FALSE WHERE id=$1::uuid`, owner)
	call(owner, b, "PUT", "", api.activateVehicle, nil, 200)
	// Concurrent device requests serialize; storage still exposes exactly one active car.
	replies := make(chan int, 2)
	for _, car := range []string{a, b} {
		go func(car string) {
			r := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(r)
			c.Request = httptest.NewRequest("PUT", "/test", nil)
			c.Set("authenticated-user", gatewayauth.User{ID: owner, Roles: []string{"driver"}})
			c.Params = gin.Params{{Key: "vehicleID", Value: car}}
			api.activateVehicle(c)
			replies <- r.Code
		}(car)
	}
	for range 2 {
		if code := <-replies; code != 200 {
			t.Fatalf("concurrent switch status=%d", code)
		}
	}
	call(owner, b, "PUT", "", api.activateVehicle, nil, 200)
	var sedan, suv bool
	if e = p.QueryRow(ctx, `SELECT driver_category_eligible($1::uuid,'sedan'),driver_category_eligible($1::uuid,'suv')`, owner).Scan(&sedan, &suv); e != nil || sedan || !suv {
		t.Fatal("inactive vehicle category used for dispatch", e)
	}
	var plate string
	if e = p.QueryRow(ctx, `SELECT car_plate FROM drivers WHERE id=$1::uuid`, owner).Scan(&plate); e != nil || plate != "CAR-B" {
		t.Fatalf("denormalized plate=%s err=%v", plate, e)
	}
	var count int
	if e = p.QueryRow(ctx, `SELECT count(*) FROM driver_vehicles WHERE driver_id=$1::uuid`, owner).Scan(&count); e != nil || count != 1 {
		t.Fatal("active compatibility view not singular")
	}
	// A scoped request must not leak its selection into subsequent pooled queries.
	call(owner, a, "GET", "?vehicleId="+a, api.vehicle, nil, 200)
	if e = p.QueryRow(ctx, `SELECT plate FROM driver_vehicles WHERE driver_id=$1::uuid`, owner).Scan(&plate); e != nil || plate != "CAR-B" {
		t.Fatalf("scope leaked: %s %v", plate, e)
	}
	call(owner, "", "POST", "", api.addVehicle, map[string]any{"make": "Toyota", "model": "Corolla", "year": 2022, "plate": "C A R-A"}, 409)
	for _, plate := range []string{"CAR-C", "CAR-D", "CAR-E"} {
		call(owner, "", "POST", "", api.addVehicle, map[string]any{"make": "Toyota", "model": "Corolla", "year": 2022, "plate": plate}, 201)
	}
	call(owner, "", "POST", "", api.addVehicle, map[string]any{"make": "Toyota", "model": "Corolla", "year": 2022, "plate": "CAR-F"}, 409)
	fare, trip := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route)VALUES($1::uuid,$2::uuid,'suv',5000,'{}')`, fare, rider)
	exec(`INSERT INTO trips(id,rider_id,ride_fare_id,status)VALUES($1::uuid,$2::uuid,$3::uuid,'pending')`, trip, rider, fare)
	exec(`INSERT INTO driver_assignments(trip_id,driver_id,attempt,status,expires_at,trip_payload)VALUES($1::uuid,$2::uuid,1,'offered',NOW()+INTERVAL '1 minute','{}')`, trip, owner)
	call(owner, a, "PUT", "", api.activateVehicle, nil, 409)
	exec(`UPDATE driver_vehicle_records SET plate='EDITED-AFTER-OFFER' WHERE id=$1::uuid`, b)
	exec(`UPDATE driver_assignments SET status='accepted' WHERE trip_id=$1::uuid`, trip)
	exec(`UPDATE trips SET assigned_driver_id=$2::uuid,status='accepted' WHERE id=$1::uuid`, trip, owner)
	call(owner, a, "PUT", "", api.activateVehicle, nil, 409)
	exec(`UPDATE driver_vehicle_records SET plate='LATER-B' WHERE id=$1::uuid`, b)
	if e = p.QueryRow(ctx, `SELECT accepted_vehicle_snapshot->>'plate' FROM trips WHERE id=$1::uuid`, trip).Scan(&plate); e != nil || plate != "CAR-B" {
		t.Fatalf("trip car changed: %s %v", plate, e)
	}
	var bound string
	if e = p.QueryRow(ctx, `SELECT accepted_vehicle_id::text FROM trips WHERE id=$1::uuid`, trip).Scan(&bound); e != nil || bound != b {
		t.Fatal("trip not bound to offered vehicle")
	}
	exec(`UPDATE driver_profiles SET admin_status='rejected' WHERE driver_id=$1::uuid`, owner)
	call(admin, b, "POST", "?vehicleId="+b, staff.reviewDriver, map[string]any{"status": "approved", "reason": "Vehicle only review"}, 200)
	var account string
	if e = p.QueryRow(ctx, `SELECT admin_status FROM driver_profiles WHERE driver_id=$1::uuid`, owner).Scan(&account); e != nil || account != "rejected" {
		t.Fatal("vehicle review lifted account restriction", e)
	}

	// Additional cars remain a post-approval flow, not an onboarding requirement.
	call(other, "", "POST", "", api.addVehicle, map[string]any{"make": "Honda", "model": "Civic", "year": 2020, "plate": "FIRST-PENDING"}, 201)
	call(other, "", "POST", "", api.addVehicle, map[string]any{"make": "Honda", "model": "Accord", "year": 2021, "plate": "SECOND-BLOCKED"}, 403)

}
