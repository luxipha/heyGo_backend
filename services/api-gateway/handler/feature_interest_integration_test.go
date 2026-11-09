package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/ideas"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFeatureInterestPublishAndSubscribe(t *testing.T) {
	dsn := os.Getenv("FEATURE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated feature database required")
	}
	ctx := context.Background()
	pool, e := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = shareddb.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	staff, rider, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO admin_users(id,username,password_hash)VALUES($1::uuid,$1,'test')`, staff)
	for _, id := range []string{rider, other} {
		exec(`INSERT INTO users(id,email,email_verified,roles)VALUES($1::uuid,$2,true,ARRAY['rider'])`, id, id+"@example.com")
	}
	var publishes, votes atomic.Int32
	var fail atomic.Bool
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing bridge auth")
		}
		if strings.Contains(r.URL.Path, "/features/") {
			publishes.Add(1)
			w.Write([]byte(`{"id":21,"published":true}`))
			return
		}
		votes.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"voted":true}`))
	}))
	defer remote.Close()
	old := newIdeasClient
	newIdeasClient = func() (*ideas.Client, error) {
		return &ideas.Client{Base: remote.URL, Token: "test-token", HTTP: remote.Client()}, nil
	}
	defer func() { newIdeasClient = old }()
	api := &adminAPI{pool: pool}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/admin", func(c *gin.Context) {
		role := c.GetHeader("Test-Role")
		if role == "" {
			role = "super_admin"
		}
		c.Set("admin", adminIdentity{ID: staff, Role: role})
		c.Set("admin-csrf", "test-csrf")
		c.Next()
	}, api.authorize)
	registerAdminFeatureInterest(g, api)
	user := r.Group("", func(c *gin.Context) {
		c.Set("authenticated-user", gatewayauth.User{ID: c.GetHeader("X-Rider"), Roles: []string{"rider"}})
		c.Next()
	})
	registerFeatureInterest(user, pool)
	call := func(who, method, path, body string, csrf bool, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Rider", who)
		req.Header.Set("Test-Role", role)
		if csrf {
			req.Header.Set("X-CSRF-Token", "test-csrf")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	base := "/admin/promotions/features"
	draft := `{"key":"go_luxury","title":"Go Luxury","description":"Help shape a future service","interestTags":["Airport runs"],"question":"When would you book Luxury?","tagline":"Premium cars","bullets":["Fixed fare"],"reason":"Create launch interest"}`
	if w := call("", "POST", base, draft, false, ""); w.Code != 403 {
		t.Fatal("CSRF", w.Code, w.Body.String())
	}
	if w := call("", "GET", base, "", false, "support"); w.Code != 403 {
		t.Fatal("RBAC", w.Code, w.Body.String())
	}
	if w := call("", "POST", base, draft, true, ""); w.Code != 200 {
		t.Fatal("create", w.Code, w.Body.String())
	}
	for i := 0; i < 2; i++ {
		if w := call("", "POST", base+"/go_luxury/publish", `{"reason":"Publish the idea"}`, true, ""); w.Code != 200 {
			t.Fatal("publish", w.Body.String())
		}
	}
	if publishes.Load() != 1 {
		t.Fatal("published twice", publishes.Load())
	}
	if w := call(rider, "POST", "/rider/features/go_luxury/interest", `{"interestTag":"Airport runs"}`, false, ""); w.Code != 200 {
		t.Fatal("answer vote", w.Body.String())
	}
	if w := call(rider, "GET", "/rider/features/go_luxury", "", false, ""); !strings.Contains(w.Body.String(), `"subscribed":false`) || !strings.Contains(w.Body.String(), `"voted":true`) || !strings.Contains(w.Body.String(), "When would you book Luxury?") {
		t.Fatal("content or separate preference", w.Body.String())
	}
	for i := 0; i < 2; i++ {
		if w := call(rider, "POST", "/rider/features/go_luxury/notify", `{"interestTag":"Airport runs"}`, false, ""); w.Code != 200 {
			t.Fatal("notify", w.Code, w.Body.String())
		}
	}
	if votes.Load() != 1 {
		t.Fatal("repeat submission", votes.Load())
	}
	w := call(rider, "GET", "/rider/features/go_luxury", "", false, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"subscribed":true`) {
		t.Fatal("persistent state", w.Body.String())
	}
	if w = call(other, "GET", "/rider/features/go_luxury", "", false, ""); !strings.Contains(w.Body.String(), `"subscribed":false`) {
		t.Fatal("foreign state", w.Body.String())
	}
	fail.Store(true)
	if w = call(other, "POST", "/rider/features/go_luxury/notify", `{}`, false, ""); w.Code != 503 {
		t.Fatal("unconfirmed remote", w.Body.String())
	}
	var count int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM rider_feature_subscriptions WHERE status='confirmed'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("false count", count, e)
	}
	fail.Store(false)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- call(other, "POST", "/rider/features/go_luxury/notify", `{}`, false, "").Code
		}()
	}
	wg.Wait()
	close(results)
	for code := range results {
		if code != 200 {
			t.Fatal("concurrent retry", code)
		}
	}
	if votes.Load() != 3 {
		t.Fatal("duplicate remote vote on concurrent retry", votes.Load())
	}
	if w = call("", "GET", base, "", false, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"subscribers":2`) {
		t.Fatal("counts", w.Body.String())
	}
	if w = call(other, "POST", "/rider/features/go_luxury/notify", `{"interestTag":"injected"}`, false, ""); w.Code != 400 {
		t.Fatal("invalid tag", w.Body.String())
	}
	if w = call(rider, "DELETE", "/rider/features/go_luxury/notify", "", false, ""); w.Code != 200 {
		t.Fatal("unsubscribe", w.Body.String())
	}
	if w = call(rider, "GET", "/rider/features/go_luxury", "", false, ""); !strings.Contains(w.Body.String(), `"subscribed":false`) || !strings.Contains(w.Body.String(), `"voted":true`) {
		t.Fatal("unsubscribe erased vote", w.Body.String())
	}
	for i := 0; i < 2; i++ {
		if w = call("", "POST", base+"/go_luxury/launch", `{"reason":"Feature is available now"}`, true, ""); w.Code != 200 {
			t.Fatal("launch", w.Body.String())
		}
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM rider_notifications WHERE source_event_id='feature-launch:go_luxury'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("launch opt-out/idempotence", count, e)
	}
	if w = call("", "POST", base+"/go_luxury/disable", `{"reason":"Close interest list"}`, true, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = call(rider, "GET", "/rider/features/go_luxury", "", false, ""); w.Code != 404 {
		t.Fatal("disabled visible", w.Body.String())
	}
	var raw map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
}
