package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/driverstate"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestManualIdentitySubmissionAndStaffCategory(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles,email_verified) VALUES($1::UUID,$2,$2,ARRAY['driver'],TRUE)`, id, "manual-flow:"+id); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::UUID`, id)
	if _, err = pool.Exec(ctx, `INSERT INTO driver_profiles(driver_id) VALUES($1::UUID)`, id); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	middleware := gatewayauth.NewMiddleware(contractTestVerifier{}, safetyContactTestUsers{id: id})
	registerDriverRoutes(router.Group("/", middleware.Authenticate), pool, &pinEvidenceStore{newPrivacyMemoryStore()})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	assertStatus := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	manualStatus := func() string {
		t.Helper()
		w := call("GET", "/driver/documents", "")
		assertStatus(w, 200)
		var body struct {
			Data []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, item := range body.Data {
			if item.Type == "manual_identity" {
				return item.Status
			}
		}
		t.Fatal("missing identity")
		return ""
	}
	if got := manualStatus(); got != "required" {
		t.Fatalf("new identity should require submission, got %s", got)
	}
	state, err := driverstate.Read(ctx, pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Requirements[0].Status != "required" {
		t.Fatal("onboarding falsely reports submitted identity")
	}
	uploads := map[string]string{}
	for _, kind := range []string{"driver_picture", "selfie_with_id", "id_document"} {
		upload := uuid.NewString()
		uploads[kind] = upload
		if _, err = pool.Exec(ctx, `INSERT INTO driver_document_uploads(id,driver_id,type,side,storage_key,content_type,size_bytes,expires_at) VALUES($1::UUID,$2::UUID,$3,'document',$1,'image/jpeg',100,NOW()+INTERVAL '10 minutes')`, upload, id, kind); err != nil {
			t.Fatal(err)
		}
	}
	// A failure after processing earlier photos must roll back the entire submission.
	validID := uploads["id_document"]
	uploads["id_document"] = uuid.NewString()
	incomplete, _ := json.Marshal(map[string]any{"firstName": "Test", "lastName": "Driver", "ninLast4": "1234", "dateOfBirth": "1995-04-12", "residentialAddress": "12 Test Street, Lagos", "evidenceUploadIds": uploads})
	assertStatus(call("PUT", "/driver/identity", string(incomplete)), 422)
	var documents, consumed int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_documents WHERE driver_id=$1::UUID`, id).Scan(&documents); err != nil || documents != 0 {
		t.Fatalf("partial documents persisted: %d, %v", documents, err)
	}
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_document_uploads WHERE driver_id=$1::UUID AND consumed_at IS NOT NULL`, id).Scan(&consumed); err != nil || consumed != 0 {
		t.Fatalf("partial uploads consumed: %d, %v", consumed, err)
	}
	uploads["id_document"] = validID
	missingAddress, _ := json.Marshal(map[string]any{"firstName": "Test", "lastName": "Driver", "ninLast4": "1234", "dateOfBirth": "1995-04-12", "evidenceUploadIds": uploads})
	assertStatus(call("PUT", "/driver/identity", string(missingAddress)), 422)
	payload, _ := json.Marshal(map[string]any{"firstName": "Test", "lastName": "Driver", "ninLast4": "1234", "dateOfBirth": "1995-04-12", "residentialAddress": "12 Test Street, Lagos", "evidenceUploadIds": uploads})
	assertStatus(call("PUT", "/driver/identity", string(payload)), 200)
	identityResponse := call("GET", "/driver/identity", "")
	assertStatus(identityResponse, 200)
	var identityData struct {
		Data identityProfile `json:"data"`
	}
	if err = json.Unmarshal(identityResponse.Body.Bytes(), &identityData); err != nil || identityData.Data.DateOfBirth != "1995-04-12" || identityData.Data.Age == nil || identityData.Data.ResidentialAddress != "12 Test Street, Lagos" {
		t.Fatal("date of birth and calculated age must round trip")
	}

	assertStatus(call("PUT", "/driver/identity", string(payload)), 409)
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_documents WHERE driver_id=$1::UUID AND status='under_review'`, id).Scan(&documents); err != nil || documents != 3 {
		t.Fatalf("expected three photos awaiting review: %d, %v", documents, err)
	}

	profileResponse := call("GET", "/driver/profile", "")
	assertStatus(profileResponse, 200)
	var profileResult struct {
		Data struct {
			DisplayName string `json:"displayName"`
		} `json:"data"`
	}
	if err := json.Unmarshal(profileResponse.Body.Bytes(), &profileResult); err != nil || profileResult.Data.DisplayName != "Test Driver" {
		t.Fatalf("profile must show submitted identity name: %s", profileResponse.Body.String())
	}
	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest("GET", "/admin/partners?q=Test&status=all&limit=100", nil)
	(&adminAPI{pool: pool}).listPartners(listContext)
	assertStatus(listRecorder, 200)
	var listed struct {
		Data []struct {
			ID                string `json:"driverId"`
			Name              string `json:"displayName"`
			ApplicationNumber string `json:"applicationNumber"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	foundName := false
	for _, item := range listed.Data {
		if item.ID == id {
			foundName = item.Name == "Test Driver"
			if !regexp.MustCompile(`^HGD-[0-9]{12}$`).MatchString(item.ApplicationNumber) {
				t.Fatal("missing application number")
			}
			var stored string
			if err := pool.QueryRow(ctx, `SELECT application_number FROM driver_profiles WHERE driver_id=$1::UUID`, id).Scan(&stored); err != nil || stored != item.ApplicationNumber {
				t.Fatal("application reference differs from persisted value")
			}
		}
	}
	if !foundName {
		t.Fatal("verification queue did not show submitted identity name")
	}
	if got := manualStatus(); got != "pending" {
		t.Fatalf("submitted identity got %s", got)
	}
	if _, err = pool.Exec(ctx, `UPDATE driver_profiles SET manual_verification_status='approved' WHERE driver_id=$1::UUID`, id); err != nil {
		t.Fatal(err)
	}
	// Unreviewed evidence cannot become a profile image, even with a legacy manual approval.
	portrait := func() string {
		response := call("GET", "/driver/profile", "")
		assertStatus(response, 200)
		var result struct {
			Data struct {
				PhotoURL string `json:"photoUrl"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Data.PhotoURL
	}
	if portrait() != "" {
		t.Fatal("unreviewed portrait exposed")
	}
	if _, err = pool.Exec(ctx, `UPDATE driver_documents SET status='approved' WHERE driver_id=$1::UUID AND type IN ('id_document','selfie_with_id')`, id); err != nil {
		t.Fatal(err)
	}
	if portrait() != "" {
		t.Fatal("private ID or selfie used as avatar")
	}
	if _, err = pool.Exec(ctx, `UPDATE driver_documents SET status='approved' WHERE driver_id=$1::UUID AND type='driver_picture'`, id); err != nil {
		t.Fatal(err)
	}
	if got := portrait(); !strings.Contains(got, uploads["driver_picture"]) {
		t.Fatal("approved driver picture missing from profile")
	}
	state, err = driverstate.Read(ctx, pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Requirements[0].Complete || state.EligibleToDrive {
		t.Fatal("manual approval should complete identity but must not grant driving eligibility")
	}
	assertStatus(call("PATCH", "/driver/vehicle", `{"make":"Toyota","model":"Corolla","year":2020,"color":"Black","plate":"TEST-123"}`), 200)
	var category string
	if err = pool.QueryRow(ctx, `SELECT package_slug FROM driver_vehicles WHERE driver_id=$1::UUID`, id).Scan(&category); err != nil {
		t.Fatal(err)
	}
	if category != "" {
		t.Fatal("category assigned before staff review")
	}
	assertStatus(call("PUT", "/driver/vehicle-package", `{"packageSlug":"suv"}`), 403)
}
