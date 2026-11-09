package handler

import (
	"bytes"
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

func TestVerificationFaceMatchReuploadAndQueue(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	api := &adminAPI{pool: pool}
	admin, driver, earlier := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$1,'test')`, admin)
	for i, id := range []string{earlier, driver} {
		exec(`INSERT INTO users(id,roles) VALUES($1::UUID,ARRAY['driver'])`, id)
		exec(`INSERT INTO driver_profiles(driver_id,created_at) VALUES($1::UUID,NOW()-make_interval(days=>$2))`, id, 30-i)
	}
	request := func(method, target, driverID, documentID string, body any, handler gin.HandlerFunc, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(method, target, bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "driverID", Value: driverID}, {Key: "documentID", Value: documentID}}
		c.Set("admin", adminIdentity{ID: admin, Username: "staff"})
		handler(c)
		if recorder.Code != want {
			t.Fatalf("%s %s: want %d got %d: %s", method, target, want, recorder.Code, recorder.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &out)
		return out
	}
	document := func(kind string, minutesAgo int) string {
		id := uuid.NewString()
		exec(`INSERT INTO driver_documents_records(id,driver_id,type,status,storage_key,submitted_at) VALUES($1::UUID,$2::UUID,$3,'under_review','test',NOW()-make_interval(mins=>$4))`, id, driver, kind, minutesAgo)
		return id
	}
	faceMatch := func() string {
		t.Helper()
		detail := request(http.MethodGet, "/admin/drivers/"+driver, driver, "", nil, api.driverReview, 200)["data"].(map[string]any)
		if detail["appliedAt"] == nil {
			t.Fatal("driver detail is missing appliedAt")
		}
		return detail["manualVerification"].(map[string]any)["faceMatch"].(string)
	}

	// Face match needs both photos, is stored, and expires when a photo is replaced.
	request(http.MethodPut, "/face-match", driver, "", map[string]string{"result": "match"}, api.recordFaceMatch, 409)
	document("selfie_with_id", 10)
	document("id_document", 10)
	request(http.MethodPut, "/face-match", driver, "", map[string]string{"result": "maybe"}, api.recordFaceMatch, 400)
	if got := faceMatch(); got != "not_reviewed" {
		t.Fatalf("faceMatch before review = %q", got)
	}
	request(http.MethodPut, "/face-match", driver, "", map[string]string{"result": "match"}, api.recordFaceMatch, 200)
	if got := faceMatch(); got != "match" {
		t.Fatalf("faceMatch after review = %q", got)
	}
	document("selfie_with_id", 1)
	if got := faceMatch(); got != "not_reviewed" {
		t.Fatalf("faceMatch after a new selfie = %q", got)
	}

	// Reupload sends the current document back once, flags it, and notifies the driver.
	licence := document("driver_license", 5)
	request(http.MethodPost, "/reupload", driver, licence, map[string]string{"reason": " "}, api.requestDocumentReupload, 400)
	request(http.MethodPost, "/reupload", driver, licence, map[string]string{"reason": "Photo is blurry"}, api.requestDocumentReupload, 200)
	var status, reason string
	var flagged bool
	if err = pool.QueryRow(ctx, `SELECT status,rejection_reason,COALESCE((metadata->>'reuploadRequested')::boolean,FALSE) FROM driver_documents_records WHERE id=$1::UUID`, licence).Scan(&status, &reason, &flagged); err != nil || status != "rejected" || reason != "Photo is blurry" || !flagged {
		t.Fatalf("document after reupload: %s %q %v %v", status, reason, flagged, err)
	}
	var title string
	if err = pool.QueryRow(ctx, `SELECT title FROM driver_notifications WHERE driver_id=$1::UUID ORDER BY id DESC LIMIT 1`, driver).Scan(&title); err != nil || !strings.Contains(title, "again") {
		t.Fatalf("driver notification title = %q err=%v", title, err)
	}
	request(http.MethodPost, "/reupload", driver, licence, map[string]string{"reason": "Again"}, api.requestDocumentReupload, 409)

	// Queue: oldest first by application time, with progress counts and appliedAt.
	list := request(http.MethodGet, "/admin/drivers?status=all&sort=oldest&progress=1&limit=100", "", "", nil, api.listPartners, 200)
	order := []string{}
	for _, raw := range list["data"].([]any) {
		item := raw.(map[string]any)
		if id := item["driverId"].(string); id == earlier || id == driver {
			order = append(order, id)
			if item["appliedAt"] == nil || item["requirementsRequired"] != float64(7) {
				t.Fatalf("queue item missing appliedAt or progress: %v", item)
			}
		}
	}
	if len(order) != 2 || order[0] != earlier {
		t.Fatalf("oldest-first order = %v, want earlier applicant first", order)
	}
	request(http.MethodGet, "/admin/drivers?sort=newest", "", "", nil, api.listPartners, 400)

	// awaiting_review covers pending drivers and approved drivers with a new vehicle to review.
	exec(`UPDATE driver_profiles SET admin_status='approved' WHERE driver_id=$1::UUID`, earlier)
	exec(`INSERT INTO driver_vehicle_records(id,driver_id,make,model,plate,package_slug,review_status,approval_status)VALUES($1::uuid,$2::uuid,'Kia','Rio',$1,'sedan','pending','pending')`, uuid.NewString(), earlier)
	queued := map[string]bool{}
	for _, raw := range request(http.MethodGet, "/admin/drivers?status=awaiting_review&limit=100", "", "", nil, api.listPartners, 200)["data"].([]any) {
		queued[raw.(map[string]any)["driverId"].(string)] = true
	}
	if !queued[earlier] || !queued[driver] {
		t.Fatalf("awaiting_review queue missing the approved driver with a pending vehicle or the pending driver: %v", queued)
	}
	summary := request(http.MethodGet, "/admin/drivers/summary", "", "", nil, api.partnerSummary, 200)["data"].(map[string]any)
	if summary["awaitingReview"].(float64) <= summary["pending"].(float64) {
		t.Fatalf("summary awaitingReview = %v, pending = %v", summary["awaitingReview"], summary["pending"])
	}
}
