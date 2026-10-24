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
	"github.com/luxipha/heyGo_backend/shared/bugreports"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

type fakeBugProvider struct {
	calls int
	fail  bool
	last  bugreports.Input
}

func (*fakeBugProvider) Name() string { return "test-bug-provider" }
func (p *fakeBugProvider) Create(_ context.Context, in bugreports.Input) (string, error) {
	p.calls++
	p.last = in
	if p.fail {
		return "", bugreports.ErrUnavailable
	}
	return fmt.Sprint(p.calls), nil
}
func (*fakeBugProvider) Get(_ context.Context, id string) (bugreports.Report, error) {
	return bugreports.Report{ID: id, Title: "App freezes", Description: "App freezes when booking", Status: "open", Comments: []bugreports.Comment{}}, nil
}
func (*fakeBugProvider) Comment(context.Context, string, string) error   { return nil }
func (*fakeBugProvider) SetStatus(context.Context, string, string) error { return nil }

func TestBugReportsOwnershipDeliveryAndUnplug(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	owner, other, admin := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, other} {
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1,$2,$3,ARRAY['rider'])`, id, "bug-test:"+id, "bug-human:"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1,$2,'unused','support')`, admin, "bug-staff:"+admin); err != nil {
		t.Fatal(err)
	}
	defer func() {
		pool.Exec(ctx, `DELETE FROM admin_audit WHERE admin_id=$1`, admin)
		pool.Exec(ctx, `DELETE FROM bug_report_links WHERE owner_id IN ($1,$2)`, owner, other)
		pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1,$2)`, owner, other)
		pool.Exec(ctx, `DELETE FROM admin_users WHERE id=$1`, admin)
	}()
	p := &fakeBugProvider{}
	a := &bugReportAPI{pool: pool, provider: p}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	for _, u := range []struct{ prefix, id, role string }{{"", owner, "rider"}, {"/other", other, "rider"}, {"/driver", other, "driver"}} {
		g := r.Group(u.prefix, gatewayauth.NewMiddleware(contractTestVerifier{}, supportTestUsers{id: u.id, role: u.role}).Authenticate)
		g.POST("/bug-reports", a.create)
		g.GET("/bug-reports", a.list)
		g.GET("/bug-reports/:reportID", a.detail)
	}
	staffAPI := &adminAPI{pool: pool}
	staff := r.Group("/admin", func(c *gin.Context) { c.Set("admin", adminIdentity{ID: admin, Role: "support"}); c.Next() }, staffAPI.authorize)
	staff.GET("/support/bug-reports", a.list)
	staff.GET("/support/bug-reports/:reportID", a.detail)
	staff.POST("/support/bug-reports/:reportID/comments", a.comment)
	staff.PATCH("/support/bug-reports/:reportID", a.setStatus)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	requestID := uuid.NewString()
	body := fmt.Sprintf(`{"requestId":%q,"title":"App freezes","description":"App freezes when booking"}`, requestID)
	created := call("POST", "/bug-reports", body)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var response struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(created.Body.Bytes(), &response)
	id := response.Data.ID
	replay := call("POST", "/bug-reports", body)
	if replay.Code != 200 || p.calls != 1 {
		t.Fatalf("duplicate create: %d calls=%d", replay.Code, p.calls)
	}
	conflict := call("POST", "/bug-reports", strings.Replace(body, "App freezes when booking", "Different report content", 1))
	if conflict.Code != 409 || p.calls != 1 {
		t.Fatal("request identity accepted changed payload")
	}
	for _, path := range []string{"/bug-reports/" + id, "/admin/support/bug-reports/" + id} {
		rec := call("GET", path, "")
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "external_id") {
			t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := call("GET", "/other/bug-reports/"+id, ""); rec.Code != 404 {
		t.Fatal("another rider accessed report")
	}
	if rec := call("GET", "/other/bug-reports", ""); rec.Code != 200 || strings.Contains(rec.Body.String(), id) {
		t.Fatal("another rider listed report")
	}
	if rec := call("GET", "/driver/bug-reports", ""); rec.Code != 200 || strings.Contains(rec.Body.String(), id) {
		t.Fatal("driver listed another account's report")
	}
	if rec := call("GET", "/driver/bug-reports/"+id, ""); rec.Code != 404 {
		t.Fatal("driver read another account's report")
	}
	driverBody := strings.Replace(body, requestID, uuid.NewString(), 1)
	driverCreated := call("POST", "/driver/bug-reports", driverBody)
	if driverCreated.Code != 201 || p.calls != 2 || p.last.Title != "[Driver] App freezes" || !strings.HasPrefix(p.last.Description, "Source: HeyGo Driver\n") {
		t.Fatalf("driver submission: %d", driverCreated.Code)
	}
	var driverResponse struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(driverCreated.Body.Bytes(), &driverResponse)
	if rec := call("GET", "/bug-reports/"+driverResponse.Data.ID, ""); rec.Code != 404 {
		t.Fatal("rider read driver's report")
	}
	if rec := call("POST", "/driver/bug-reports", driverBody); rec.Code != 200 || p.calls != 2 {
		t.Fatal("driver retry duplicated provider write")
	}

	if rec := call("POST", "/admin/support/bug-reports/"+id+"/comments", `{"body":"Please update the app"}`); rec.Code != 200 {
		t.Fatalf("comment: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("PATCH", "/admin/support/bug-reports/"+id, `{"status":"resolved"}`); rec.Code != 200 {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var audits int
	pool.QueryRow(ctx, `SELECT COUNT(*) FROM admin_audit WHERE admin_id=$1 AND target_type='bug_report'`, admin).Scan(&audits)
	if audits != 2 {
		t.Fatalf("missing staff intent audit: %d", audits)
	}
	p.fail = true
	failedBody := strings.Replace(body, requestID, uuid.NewString(), 1)
	if rec := call("POST", "/bug-reports", failedBody); rec.Code != 502 {
		t.Fatal("provider failure reported success")
	}
	if rec := call("POST", "/bug-reports", failedBody); rec.Code != 409 || p.calls != 3 {
		t.Fatal("uncertain delivery automatically resubmitted")
	}
	var external *string
	pool.QueryRow(ctx, `SELECT external_id FROM bug_report_links WHERE owner_id=$1 AND external_id IS NULL`, owner).Scan(&external)
	if external != nil {
		t.Fatal("unconfirmed report has provider reference")
	}
	a.provider = nil
	if rec := call("GET", "/bug-reports", ""); rec.Code != 503 {
		t.Fatal("unplug did not fail closed")
	}
	a.provider = p
	if rec := call("GET", "/bug-reports/"+id, ""); rec.Code != 200 {
		t.Fatal("unplug lost historical reference")
	}
}
func TestBugReportsDisabledAndAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	a := &bugReportAPI{}
	r.GET("/bug-reports", a.list)
	{
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/bug-reports", nil))
		if rec.Code != 401 {
			t.Fatalf("missing auth: %d", rec.Code)
		}
	}
	t.Setenv("BUG_REPORT_PROVIDER", "disabled")
	if newBugReportAPI(nil).provider != nil {
		t.Fatal("disabled provider was loaded")
	}
	t.Setenv("BUG_REPORT_PROVIDER", "mantis")
	t.Setenv("MANTIS_URL", "http://invalid")
	if newBugReportAPI(nil).provider != nil {
		t.Fatal("invalid provider was loaded")
	}
}
