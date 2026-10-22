package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/storage"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type pinEvidenceStore struct{ *privacyMemoryStore }

func (s *pinEvidenceStore) Head(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{Size: 100, ContentType: "image/jpeg"}, nil
}

func TestDriverDocumentPINAndReadOnlyAccess(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	t.Setenv("OTP_PEPPER", strings.Repeat("p", 40))
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	owner, other := uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, other} {
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles,email_verified) VALUES($1::UUID,$2,$2,ARRAY['driver'],TRUE)`, id, "pin-test:"+id); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO driver_profiles(driver_id,manual_verification_status) VALUES($1::UUID,'approved')`, id); err != nil {
			t.Fatal(err)
		}
	}
	defer pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::UUID[])`, []string{owner, other})
	_, err = pool.Exec(ctx, `INSERT INTO driver_identity_profiles(driver_id,first_name,last_name,nin_last4) VALUES($1::UUID,'Test','Driver','1234')`, owner)
	if err != nil {
		t.Fatal(err)
	}
	store := &pinEvidenceStore{newPrivacyMemoryStore()}
	gin.SetMode(gin.TestMode)
	call := func(who, method, path, body string) *httptest.ResponseRecorder {
		r := gin.New()
		auth := gatewayauth.NewMiddleware(contractTestVerifier{}, safetyContactTestUsers{id: who})
		registerDriverRoutes(r.Group("/", auth.Authenticate), pool, store)
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	check := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	check(call(owner, "GET", "/driver/identity", ""), 403)
	check(call(owner, "POST", "/driver/documents/identity/view", `{"pin":"1234"}`), 409)
	check(call(owner, "PUT", "/driver/document-pin", `{"newPin":"1234"}`), 200)
	var hash string
	if err = pool.QueryRow(ctx, `SELECT pin_hash FROM driver_document_pins WHERE driver_id=$1::UUID`, owner).Scan(&hash); err != nil || hash == "1234" || !strings.HasPrefix(hash, "$2") {
		t.Fatal("PIN not stored as hash")
	}
	check(call(owner, "POST", "/driver/documents/identity/view", `{"pin":"0000"}`), 403)
	w := call(owner, "POST", "/driver/documents/identity/view", `{"pin":"1234"}`)
	check(w, 200)
	if !strings.Contains(w.Body.String(), "Test") {
		t.Fatal("identity missing")
	}
	check(call(owner, "PUT", "/driver/identity", `{"firstName":"Changed"}`), 409)
	doc := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO driver_documents(id,driver_id,type,status,storage_key,metadata,expires_at) VALUES($1::UUID,$2::UUID,'driver_license','approved','private-front','{"files":[{"side":"front","storageKey":"private-front","contentType":"image/jpeg"}]}',NOW()+INTERVAL '1 day')`, doc, owner)
	if err != nil {
		t.Fatal(err)
	}
	check(call(other, "POST", "/driver/documents/"+doc+"/view", `{"pin":"1234"}`), 404)
	check(call(owner, "POST", "/driver/documents/"+doc+"/view", `{"pin":"1234"}`), 200)
	if store.presignTTL != 2*time.Minute {
		t.Fatal("preview lifetime incorrect")
	}
	pool.Exec(ctx, `UPDATE driver_documents SET status='under_review' WHERE id=$1::UUID`, doc)
	check(call(owner, "POST", "/driver/documents/"+doc+"/view", `{"pin":"1234"}`), 409)
	pool.Exec(ctx, `UPDATE driver_documents SET status='approved',expires_at=NOW()-INTERVAL '1 day' WHERE id=$1::UUID`, doc)
	check(call(owner, "POST", "/driver/documents/"+doc+"/view", `{"pin":"1234"}`), 409)
	var list struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	w = call(owner, "GET", "/driver/documents", "")
	check(w, 200)
	if json.Unmarshal(w.Body.Bytes(), &list) != nil {
		t.Fatal("invalid list")
	}
	found := false
	for _, item := range list.Data {
		if item.ID == doc {
			found = item.Status == "expired"
		}
	}
	if !found {
		t.Fatal("expired state missing")
	}
	// Expired and rejected replacements create new records; old evidence stays in history.
	renew := func(previous string) string {
		uploads := []string{uuid.NewString(), uuid.NewString()}
		for i, side := range []string{"front", "back"} {
			if _, err := pool.Exec(ctx, `INSERT INTO driver_document_uploads(id,driver_id,type,side,storage_key,content_type,size_bytes,expires_at) VALUES($1::UUID,$2::UUID,'driver_license',$3,$4,'image/jpeg',100,NOW()+INTERVAL '10 minutes')`, uploads[i], owner, side, "replacement-"+uploads[i]); err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := json.Marshal(map[string]any{"type": "driver_license", "uploadIds": uploads})
		response := call(owner, "POST", "/driver/documents/"+previous+"/resubmit", string(raw))
		check(response, 201)
		var latest string
		if err := pool.QueryRow(ctx, `SELECT id::TEXT FROM driver_documents WHERE driver_id=$1::UUID ORDER BY submitted_at DESC,id DESC LIMIT 1`, owner).Scan(&latest); err != nil {
			t.Fatal(err)
		}
		return latest
	}
	pending := renew(doc)
	check(call(owner, "POST", "/driver/documents/"+pending+"/view", `{"pin":"1234"}`), 409)
	pool.Exec(ctx, `UPDATE driver_documents SET status='rejected' WHERE id=$1::UUID`, pending)
	renew(pending)
	var records int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_documents WHERE driver_id=$1::UUID`, owner).Scan(&records); err != nil || records != 3 {
		t.Fatal("replacement did not preserve history")
	}
	for i := 0; i < 5; i++ {
		want := 403
		if i == 4 {
			want = 429
		}
		check(call(owner, "POST", "/driver/documents/identity/view", `{"pin":"0000"}`), want)
	}
	check(call(owner, "POST", "/driver/documents/identity/view", `{"pin":"1234"}`), 429)
	pool.Exec(ctx, `UPDATE driver_document_pins SET locked_until=NOW()-INTERVAL '1 minute' WHERE driver_id=$1::UUID`, owner)
	check(call(owner, "PUT", "/driver/document-pin", `{"currentPin":"1234","newPin":"5678"}`), 200)
	check(call(owner, "POST", "/driver/documents/identity/view", `{"pin":"1234"}`), 403)
	check(call(owner, "POST", "/driver/documents/identity/view", `{"pin":"5678"}`), 200)
}
