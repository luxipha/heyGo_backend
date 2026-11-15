package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestOnboardingReadJourney(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	staff := uuid.NewString()
	prefix := "caller-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::UUID,$2,'unused','onboarding')`, staff, prefix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM admin_users WHERE id=$1::UUID`, staff)
		for _, id := range ids {
			_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::UUID`, id)
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO admin_user_roles(admin_id,role) VALUES($1::UUID,'onboarding')`, staff); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		role := "driver"
		if i == 2 {
			role = "rider"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles,phone_e164,created_at) VALUES($1::UUID,$2,$2,ARRAY[$3],$4,NOW()-$5::INTERVAL)`, id, prefix+id, role, "+234800"+strings.ReplaceAll(id, "-", "")[:7], string(rune('1'+i))+" hours"); err != nil {
			t.Fatal(err)
		}
	}
	// First driver has not opened the profile form; they must still be callable.
	if _, err := pool.Exec(ctx, `INSERT INTO driver_profiles(driver_id,display_name,admin_status) VALUES($1::UUID,$2,'approved')`, ids[1], prefix); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	a := &adminAPI{pool: pool}
	g := r.Group("/admin", func(c *gin.Context) { c.Set("admin", adminIdentity{ID: staff, Role: "onboarding"}); c.Next() }, a.authorize)
	g.GET("/onboarding/drivers", a.listOnboardingDrivers)
	g.GET("/onboarding/summary", a.onboardingSummary)
	g.GET("/onboarding/drivers/:driverID", a.onboardingDriver)
	get := func(path string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var value map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	detail := get("/admin/onboarding/drivers/" + ids[0])["data"].(map[string]any)
	driver := detail["driver"].(map[string]any)
	if driver["driverId"] != ids[0] || driver["phone"] == "" || detail["driverDetailsComplete"] != false {
		t.Fatalf("new signup omitted: %v", detail)
	}
	for _, key := range []string{"identity", "documents", "links", "balanceKobo"} {
		if _, ok := detail[key]; ok {
			t.Fatalf("sensitive field %s leaked", key)
		}
	}
	completed := get("/admin/onboarding/drivers?status=completed&q=" + prefix)["data"].([]any)
	if len(completed) != 1 || completed[0].(map[string]any)["driverId"] != ids[1] {
		t.Fatalf("completed filter: %v", completed)
	}
	phone := driver["phone"].(string)
	found := get("/admin/onboarding/drivers?q=" + phone[1:] + "&status=incomplete")["data"].([]any)
	if len(found) != 1 {
		t.Fatalf("phone search: %v", found)
	}
	summary := get("/admin/onboarding/summary")["data"].(map[string]any)
	if summary["completed"].(float64) < 1 {
		t.Fatal("approved driver absent from completed total")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/admin/onboarding/drivers/"+ids[2], nil))
	if w.Code != 404 {
		t.Fatalf("rider exposed: %d", w.Code)
	}
	first := get("/admin/onboarding/drivers?status=all&limit=1")
	if next, ok := first["meta"].(map[string]any)["nextCursor"].(string); !ok || next == "" {
		t.Fatal("missing pagination cursor")
	} else {
		second := get("/admin/onboarding/drivers?status=all&limit=1&cursor=" + next)
		if first["data"].([]any)[0].(map[string]any)["driverId"] == second["data"].([]any)[0].(map[string]any)["driverId"] {
			t.Fatal("pagination repeats driver")
		}
	}
}
