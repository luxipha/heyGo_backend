package handler

import (
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
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestSupportTicketOptionalTripOwnership(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	owner, other, fare, trip := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{owner, other} {
		exec(`INSERT INTO users(id,casper_id_user_id,roles) VALUES($1::UUID,$2,ARRAY['rider'])`, id, "support-link:"+id)
	}
	defer func() {
		pool.Exec(context.Background(), `DELETE FROM support_cases WHERE opened_by=$1::UUID`, owner)
		pool.Exec(context.Background(), `DELETE FROM trips WHERE id=$1::UUID`, trip)
		pool.Exec(context.Background(), `DELETE FROM ride_fares WHERE id=$1::UUID`, fare)
		pool.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1::UUID,$2::UUID)`, owner, other)
	}()
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',5000,'{}')`, fare, owner)
	exec(`INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,'completed')`, trip, owner, fare)
	gin.SetMode(gin.TestMode)
	api := func(id string) *gin.Engine {
		r := gin.New()
		registerSupportRoutes(r.Group("/", gatewayauth.NewMiddleware(contractTestVerifier{}, supportTestUsers{id: id, role: "rider"}).Authenticate), pool, nil)
		return r
	}
	call := func(id, linked, requestID string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"topicCode": "general-other", "description": "QA trip link", "clientMessageId": requestID, "tripId": linked})
		req := httptest.NewRequest(http.MethodPost, "/support/tickets", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer test")
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		api(id).ServeHTTP(res, req)
		return res
	}
	for _, bad := range []struct {
		id, trip string
		status   int
	}{{owner, "invalid", 400}, {other, trip, 403}, {owner, uuid.NewString(), 403}} {
		if res := call(bad.id, bad.trip, uuid.NewString()); res.Code != bad.status {
			t.Fatalf("ownership status=%d body=%s", res.Code, res.Body.String())
		}
	}
	requestID := uuid.NewString()
	first := call(owner, trip, requestID)
	if first.Code != 201 {
		t.Fatalf("linked create=%d %s", first.Code, first.Body.String())
	}
	var created struct {
		Data struct {
			CaseID string `json:"caseId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/support/cases", "/support/cases/" + created.Data.CaseID, "/support/cases/" + created.Data.CaseID + "/messages"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test")
		res := httptest.NewRecorder()
		api(owner).ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("nullable human ID read %s: %d %s", path, res.Code, res.Body.String())
		}
	}
	replay := call(owner, trip, requestID)
	if replay.Code != 201 || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay=%d %s", replay.Code, replay.Body.String())
	}
	if res := call(owner, "", requestID); res.Code == 201 {
		t.Fatal("changed trip link accepted for same request ID")
	}
	if res := call(owner, "", uuid.NewString()); res.Code != 201 {
		t.Fatalf("unlinked create=%d %s", res.Code, res.Body.String())
	}
	var linked, unlinked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE trip_id=$2::UUID),count(*) FILTER(WHERE trip_id IS NULL) FROM support_cases WHERE opened_by=$1::UUID`, owner, trip).Scan(&linked, &unlinked); err != nil || linked != 1 || unlinked != 1 {
		t.Fatalf("linked=%d unlinked=%d err=%v", linked, unlinked, err)
	}
}
