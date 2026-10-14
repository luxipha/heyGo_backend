package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRiderAccountPersistencePrivacyAndOwnership(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	owner, other, driver, staff := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	phone := fmt.Sprintf("+234%010d", uint64(time.Now().UnixNano())%10000000000)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{owner, other, driver} {
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles,email,email_verified) VALUES($1::UUID,$2,$3,$4,$5,TRUE)`, id, "account-private-"+id, "account-human-"+id, []string{"rider"}, id+"@example.com")
	}
	exec(`UPDATE users SET roles=ARRAY['driver'] WHERE id=$1::UUID`, driver)
	exec(`UPDATE users SET phone_e164=$2,phone_verified_at=NOW() WHERE id=$1::UUID`, owner, phone)
	exec(`INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::UUID,$2,'unused','compliance')`, staff, "rider-privacy-"+staff)
	t.Setenv("HEYGO_ADMIN_SETTINGS_KEY", strings.Repeat("04", 32))
	files := newPrivacyMemoryStore()
	router := gin.New()
	g := router.Group("", func(c *gin.Context) {
		id := c.GetHeader("X-Test-Owner")
		role := "rider"
		if id == driver {
			role = "driver"
		}
		c.Set("authenticated-user", gatewayauth.User{ID: id, Roles: []string{role}, Email: id + "@example.com", EmailVerified: c.GetHeader("X-Unverified") == "", Phone: phone, PhoneVerified: true, CasperSubject: "account-private-" + id, HumanID: "account-human-" + id})
	})
	registerRiderAccountRoutes(g, pool, files)
	call := func(id, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Owner", id)
		router.ServeHTTP(w, req)
		return w
	}
	data := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var body struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data
	}
	if w := call(driver, "GET", "/rider/preferences", ""); w.Code != 403 {
		t.Fatal("Driver-only principal entered Rider account routes")
	}
	if w := call(owner, "GET", "/rider/preferences", ""); w.Code != 200 || data(w)["reduceMotion"] != false {
		t.Fatalf("defaults: %s", w.Body.String())
	}
	if w := call(owner, "PATCH", "/rider/preferences", `{"reduceMotion":true,"shareRideStatus":true}`); w.Code != 200 || data(w)["rideSharingEffective"] != false {
		t.Fatalf("preferences: %s", w.Body.String())
	}
	if w := call(other, "GET", "/rider/preferences", ""); data(w)["reduceMotion"] != false {
		t.Fatal("preferences leaked to another Rider")
	}
	contact := uuid.NewString()
	body := `{"fullName":"Trusted person","relationship":"Sibling","phoneNumber":"08012345678"}`
	for range 2 {
		if w := call(owner, "PUT", "/rider/trusted-contacts/"+contact, body); w.Code != 200 || data(w)["phoneNumber"] != "+2348012345678" {
			t.Fatalf("contact: %s", w.Body.String())
		}
	}
	if w := call(other, "PUT", "/rider/trusted-contacts/"+contact, body); w.Code != 404 {
		t.Fatal("foreign contact was writable")
	}
	if w := call(other, "DELETE", "/rider/trusted-contacts/"+contact, ""); w.Code != 200 {
		t.Fatal("idempotent deletion failed")
	}
	if w := call(owner, "GET", "/rider/trusted-contacts", ""); len(data(w)["items"].([]any)) != 1 {
		t.Fatal("contact was duplicated or removed by a foreign Rider")
	}
	lookup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request agent.Request
		json.NewDecoder(r.Body).Decode(&request)
		place := agent.Place{ID: request.PlaceID, Name: "Resolved place", Address: "Provider address not retained", Provider: "google", Location: agent.Coordinate{Latitude: 6.45, Longitude: 3.4}}
		json.NewEncoder(w).Encode(agent.Response{SchemaVersion: 1, State: "resolved", ConfirmationRequired: true, Place: &place, Candidates: []agent.Place{place}})
	}))
	defer lookup.Close()
	t.Setenv("RIDE_AGENT_URL", lookup.URL)
	t.Setenv("RIDE_AGENT_INTERNAL_TOKEN", strings.Repeat("x", 32))
	placeBody := `{"placeId":"stable-place-a","provider":"google","version":0}`
	first := call(owner, "PUT", "/rider/saved-places/Home", placeBody)
	second := call(owner, "PUT", "/rider/saved-places/Home", placeBody)
	if first.Code != 200 || second.Code != 200 || data(first)["id"] != data(second)["id"] {
		t.Fatalf("place retry: %s / %s", first.Body.String(), second.Body.String())
	}
	if w := call(other, "GET", "/rider/saved-places", ""); len(data(w)["items"].([]any)) != 0 {
		t.Fatal("saved place ownership failed")
	}
	if w := call(owner, "PUT", "/rider/saved-places/Home", `{"placeId":"stable-place-b","provider":"google","version":0}`); w.Code != 409 {
		t.Fatal("stale saved-place edit applied")
	}
	if w := call(owner, "GET", "/rider/saved-places", ""); strings.Contains(w.Body.String(), "Provider address") {
		t.Fatal("provider address was persisted")
	}
	t.Setenv("RIDE_AGENT_URL", "")
	if w := call(owner, "PUT", "/rider/saved-places/Work", placeBody); w.Code != 503 {
		t.Fatal("unavailable resolution was presented as saved")
	}
	if w := call(owner, "GET", "/rider/saved-places/Home/resolve", ""); w.Code != 503 {
		t.Fatal("unavailable saved metadata was fabricated")
	}
	key := bytes.Repeat([]byte{4}, 32)
	cipher, err := sealAdminSecret(key, "re_test_key")
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO admin_resend_settings(id,api_key_ciphertext,from_email,from_name,updated_by) VALUES(1,$1,'noreply@example.com','HeyGo',$2::UUID) ON CONFLICT(id) DO UPDATE SET api_key_ciphertext=EXCLUDED.api_key_ciphertext,updated_by=EXCLUDED.updated_by`, cipher, staff)
	exportID := uuid.NewString()
	request := `{"confirmed":true,"clientRequestId":"` + exportID + `"}`
	first = call(owner, "POST", "/rider/data-export", request)
	second = call(owner, "POST", "/rider/data-export", request)
	if first.Code != 202 || second.Code != 200 || data(first)["id"] != data(second)["id"] {
		t.Fatalf("export: %s / %s", first.Body.String(), second.Body.String())
	}
	if w := call(other, "GET", "/rider/data-export/"+exportID, ""); w.Code != 404 {
		t.Fatal("foreign export status leaked")
	}
	if w := call(owner, "POST", "/rider/data-export", `{"confirmed":true,"clientRequestId":"`+uuid.NewString()+`"}`); w.Code != 409 {
		t.Fatal("pending export created an untracked retry identity")
	}
	if !processNextPrivateDataExport(ctx, pool, files, key, func(_ context.Context, deliveryID, apiKey, from, name, to, subject, text, html string) error {
		if deliveryID != "rider-data-export/"+exportID || to != owner+"@example.com" {
			t.Fatal("wrong scoped recipient/key")
		}
		return nil
	}, "rider") {
		t.Fatal("Rider export was not processed")
	}
	files.mu.Lock()
	archive := append([]byte{}, files.objects["rider-data-exports/"+owner+"/"+exportID+".zip"]...)
	files.mu.Unlock()
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := z.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := io.ReadAll(reader)
	reader.Close()
	if !bytes.Contains(manifest, []byte("Trusted person")) || bytes.Contains(manifest, []byte(other)) || bytes.Contains(manifest, []byte("account-private-")) || bytes.Contains(manifest, []byte("driver_finances")) {
		t.Fatal("archive manifest failed ownership/minimization")
	}
	if w := call(owner, "GET", "/rider/data-export/"+exportID, ""); data(w)["status"] != "sent" || strings.Contains(w.Body.String(), "private.example") {
		t.Fatal("export status/link boundary failed")
	}
	exec(`UPDATE rider_data_exports SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1::UUID`, exportID)
	expirePrivateExports(ctx, pool, files, "rider_data_exports")
	if w := call(owner, "GET", "/rider/data-export/"+exportID, ""); data(w)["status"] != "expired" {
		t.Fatal("private archive expiry failed")
	}
	deletionID := uuid.NewString()
	request = `{"confirmed":true,"clientRequestId":"` + deletionID + `"}`
	if w := call(owner, "POST", "/rider/account-deletion", request); w.Code != 202 {
		t.Fatalf("deletion: %s", w.Body.String())
	}
	if w := call(owner, "POST", "/rider/account-deletion", request); w.Code != 200 {
		t.Fatal("deletion replay was not recovered")
	}
	fare, trip := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',100000,'{}')`, fare, owner)
	exec(`INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,'pending')`, trip, owner, fare)
	admin := &adminAPI{pool: pool}
	review := func(decision string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(decision))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "requestID", Value: deletionID}}
		c.Set("admin", adminIdentity{ID: staff, Role: "compliance"})
		admin.reviewAccountDeletionRequest(c)
		return w
	}
	if w := review(`{"decision":"approve"}`); w.Code != 409 {
		t.Fatalf("active Rider deletion: %s", w.Body.String())
	}
	exec(`UPDATE trips SET status='cancelled',cancelled_at=NOW() WHERE id=$1::UUID`, trip)
	if w := review(`{"decision":"approve"}`); w.Code != 200 {
		t.Fatalf("approval: %s", w.Body.String())
	}
	var active bool
	var remaining int
	if err = pool.QueryRow(ctx, `SELECT active,(SELECT COUNT(*) FROM rider_trusted_contacts WHERE rider_id=$1::UUID)+(SELECT COUNT(*) FROM rider_saved_places WHERE rider_id=$1::UUID)+(SELECT COUNT(*) FROM rider_preferences WHERE rider_id=$1::UUID) FROM users WHERE id=$1::UUID`, owner).Scan(&active, &remaining); err != nil || active || remaining != 0 {
		t.Fatalf("deletion persistence: %v %v %d", err, active, remaining)
	}
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM trips WHERE id=$1::UUID`, trip).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatal("historical trip was erased")
	}
	// Cleanup only this synthetic fixture; preserve the separate staging database.
	exec(`DELETE FROM trips WHERE id=$1::UUID`, trip)
	exec(`DELETE FROM ride_fares WHERE id=$1::UUID`, fare)
	exec(`DELETE FROM deactivated_phone_identities WHERE deletion_request_id=$1::UUID`, deletionID)
	exec(`DELETE FROM deactivated_email_identities WHERE deletion_request_id=$1::UUID`, deletionID)
	exec(`DELETE FROM deactivated_casperid_identities WHERE deletion_request_id=$1::UUID`, deletionID)
	exec(`DELETE FROM driver_account_deletion_requests WHERE id=$1::UUID`, deletionID)
	exec(`DELETE FROM rider_data_exports WHERE rider_id=$1::UUID`, owner)
	exec(`DELETE FROM admin_audit WHERE admin_id=$1::UUID`, staff)
	exec(`DELETE FROM users WHERE id=ANY($1::UUID[])`, []string{owner, other, driver})
}
