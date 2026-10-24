package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestDriverTripIssueEvidenceOwnershipAndRetry(t *testing.T) {
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
	owner, other, rider, fare, trip := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []string{owner, other, rider} {
		exec(`INSERT INTO users(id,roles,active) VALUES($1::uuid,ARRAY['driver'],TRUE)`, id)
	}
	exec(`INSERT INTO drivers(id,name,car_plate,package_slug,status) VALUES($1::uuid,'Evidence driver','TEST','sedan','on_trip')`, owner)
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::uuid,$2::uuid,'sedan',1000,'{}')`, fare, rider)
	exec(`INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status,origin_region_code) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'started','TEST')`, trip, rider, fare, owner)
	defer func() {
		pool.Exec(ctx, `DELETE FROM support_cases WHERE opened_by=ANY($1::uuid[])`, []string{owner, other})
		pool.Exec(ctx, `DELETE FROM support_uploads WHERE owner_id=ANY($1::uuid[])`, []string{owner, other})
		pool.Exec(ctx, `DELETE FROM trips WHERE id=$1::uuid`, trip)
		pool.Exec(ctx, `DELETE FROM ride_fares WHERE id=$1::uuid`, fare)
		pool.Exec(ctx, `DELETE FROM drivers WHERE id=$1::uuid`, owner)
		pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::uuid[])`, []string{owner, other, rider})
	}()
	api := supportAPI{pool: pool, files: &pinEvidenceStore{newPrivacyMemoryStore()}}
	router := gin.New()
	for _, u := range []struct{ prefix, id string }{{"", owner}, {"/foreign", other}} {
		g := router.Group(u.prefix, gatewayauth.NewMiddleware(contractTestVerifier{}, supportTestUsers{id: u.id, role: "driver"}).Authenticate)
		g.POST("/uploads", api.createUpload)
		g.POST("/trips/:tripID/issues", api.createTripIssue)
	}
	call := func(path, body string, want int) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s: %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		var v struct {
			Data map[string]any `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &v)
		return v.Data
	}
	upload := call("/uploads", `{"contentType":"image/jpeg","sizeBytes":100}`, 200)["uploadId"].(string)
	foreign := call("/foreign/uploads", `{"contentType":"image/jpeg","sizeBytes":100}`, 200)["uploadId"].(string)
	body := func(id, request string) string {
		return fmt.Sprintf(`{"topicCode":"fare","description":"Detail: Payment delay\n\nTransfer pending.","uploadIds":[%q],"clientMessageId":%q}`, id, request)
	}
	path := "/trips/" + trip + "/issues"
	call(path, body(foreign, uuid.NewString()), 400)
	call("/foreign"+path, body(foreign, uuid.NewString()), 403)
	request := uuid.NewString()
	data := call(path, body(upload, request), 201)
	caseID := data["caseId"].(string)
	var count int
	var description string
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM support_case_attachments a JOIN support_case_messages m ON m.id=a.message_id WHERE m.case_id=$1::uuid`, caseID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("attachment association count=%d err=%v", count, err)
	}
	if err = pool.QueryRow(ctx, `SELECT body FROM support_case_messages WHERE case_id=$1::uuid AND sender_kind='user'`, caseID).Scan(&description); err != nil || !strings.Contains(description, "Payment delay") {
		t.Fatal("selected quick detail missing from staff message")
	}
	replay := call(path, body(upload, request), 201)
	if replay["caseId"] != caseID {
		t.Fatal("retry created another case")
	}
	call(path, body(upload, uuid.NewString()), 400)
	expired := call("/uploads", `{"contentType":"image/jpeg","sizeBytes":100}`, 200)["uploadId"].(string)
	exec(`UPDATE support_uploads SET expires_at=NOW()-INTERVAL '1 minute' WHERE id=$1::uuid`, expired)
	call(path, body(expired, uuid.NewString()), 400)
}
