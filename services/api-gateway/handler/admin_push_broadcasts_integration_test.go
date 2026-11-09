package handler

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAdminPushBroadcastSchedulingAndFanout(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	t.Setenv("FCM_SERVICE_ACCOUNT_FILE", "")
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 6})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	staff, driver, rider, inactive := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO admin_users(id,username,password_hash) VALUES($1::UUID,$1,'test')`, staff)
	for i, id := range []string{driver, rider, inactive} {
		role := "rider"
		if i == 0 {
			role = "driver"
		}
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles,active) VALUES($1::UUID,$1,$1,ARRAY[$2],$3)`, id, role, i != 2)
	}
	exec(`INSERT INTO driver_devices(id,driver_id,platform,push_token) VALUES($1::UUID,$2::UUID,'android',$1)`, uuid.NewString(), driver)
	router := campaignTestRouter(&adminAPI{pool: pool}, staff)
	create := func(audience string) string {
		t.Helper()
		id := uuid.NewString()
		w := campaignTestCall(router, "POST", "/admin/communications/campaigns", map[string]any{"id": id, "name": "Test announcement", "category": "Push", "audience": audience, "body": "Test message", "reason": "Testing broadcasts"}, true, "super_admin")
		if w.Code != 201 {
			t.Fatalf("create %d %s", w.Code, w.Body.String())
		}
		return id
	}

	feedback := create("Riders")
	inputFeedback := map[string]any{"version": 1, "purpose": "feedback", "reason": "Testing feedback"}
	if w := campaignTestCall(router, "POST", "/admin/communications/campaigns/"+feedback+"/send", inputFeedback, true, "super_admin"); w.Code != 200 {
		t.Fatalf("feedback: %d %s", w.Code, w.Body.String())
	}
	if err := fanOutPushBroadcasts(ctx, pool, 100, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var feedbackCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM rider_notifications WHERE rider_id=$1::uuid AND type='feedback' AND title='Test announcement' AND message='Test message'`, rider).Scan(&feedbackCount); err != nil || feedbackCount != 1 {
		t.Fatalf("feedback count=%d error=%v", feedbackCount, err)
	}
	wrong := create("Drivers")
	if w := campaignTestCall(router, "POST", "/admin/communications/campaigns/"+wrong+"/send", inputFeedback, true, "super_admin"); w.Code != 400 {
		t.Fatalf("feedback audience: %d", w.Code)
	}
	campaign := create("All users")
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	input := map[string]any{"version": 1, "purpose": "announcement", "scheduledFor": at, "reason": "Testing schedule"}
	path := "/admin/communications/campaigns/" + campaign
	for i := 0; i < 2; i++ {
		w := campaignTestCall(router, "POST", path+"/send", input, true, "super_admin")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"scheduled"`) || !strings.Contains(w.Body.String(), `"providerConfigured":false`) {
			t.Fatalf("schedule %d %s", w.Code, w.Body.String())
		}
	}
	var broadcast string
	if err = pool.QueryRow(ctx, `SELECT id::TEXT FROM admin_push_broadcasts WHERE campaign_id=$1::UUID`, campaign).Scan(&broadcast); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM admin_push_recipients WHERE broadcast_id=$1::UUID AND user_id=$2::UUID`, broadcast, inactive).Scan(&count); err != nil || count != 0 {
		t.Fatal("inactive recipient included")
	}
	if err = RunPushBroadcastJobsOnce(ctx, pool, 100); err != nil {
		t.Fatal(err)
	}
	if err = fanOutPushBroadcasts(ctx, pool, 100, at.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	var status string
	pool.QueryRow(ctx, `SELECT status FROM admin_push_broadcasts WHERE id=$1::UUID`, broadcast).Scan(&status)
	if status != "scheduled" {
		t.Fatal("scheduled broadcast ran early")
	}
	// Concurrent retries must not duplicate per-user inbox entries.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- fanOutPushBroadcasts(ctx, pool, 100, at) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if err = fanOutPushBroadcasts(ctx, pool, 100, at); err != nil {
		t.Fatal(err)
	}
	source := "broadcast:" + broadcast
	for _, q := range []string{`SELECT COUNT(*) FROM driver_notifications WHERE driver_id=$1::UUID AND source_event_id=$2`, `SELECT COUNT(*) FROM rider_notifications WHERE rider_id=$1::UUID AND source_event_id=$2`} {
		owner := driver
		if strings.Contains(q, "rider_notifications") {
			owner = rider
		}
		if e := pool.QueryRow(ctx, q, owner, source).Scan(&count); e != nil || count != 1 {
			t.Fatalf("fanout count=%d err=%v", count, e)
		}
	}
	pool.QueryRow(ctx, `SELECT status FROM admin_push_broadcasts WHERE id=$1::UUID`, broadcast).Scan(&status)
	if status != "enqueued" {
		t.Fatal("fanout not complete")
	}
	w := campaignTestCall(router, "POST", path+"/cancel", map[string]string{"reason": "Testing cancellation"}, true, "super_admin")
	if w.Code != 409 {
		t.Fatal("completed broadcast cancellable")
	}
	w = campaignTestCall(router, "GET", path+"/broadcast", nil, false, "super_admin")
	var result map[string]any
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || !strings.Contains(w.Body.String(), `"sentDevices":0`) {
		t.Fatal("unconfirmed sends reported delivered")
	}
	second := create("Drivers")
	secondPath := "/admin/communications/campaigns/" + second
	w = campaignTestCall(router, "POST", secondPath+"/send", map[string]any{"version": 1, "purpose": "marketing", "reason": "Test marketing"}, false, "super_admin")
	if w.Code != 403 {
		t.Fatal("CSRF not enforced")
	}
	w = campaignTestCall(router, "POST", secondPath+"/send", map[string]any{"version": 1, "purpose": "marketing", "reason": "Test marketing"}, true, "operations")
	if w.Code != 403 {
		t.Fatal("permission not enforced")
	}
	w = campaignTestCall(router, "POST", secondPath+"/send", map[string]any{"version": 1, "purpose": "marketing", "reason": "Test marketing"}, true, "super_admin")
	if w.Code != 200 {
		t.Fatalf("queue %s", w.Body.String())
	}
	if err = RunPushBroadcastJobsOnce(ctx, pool, 100); err != nil {
		t.Fatal(err)
	}
	var waiting string
	if err = pool.QueryRow(ctx, `SELECT status FROM admin_push_broadcasts WHERE campaign_id=$1::UUID`, second).Scan(&waiting); err != nil || waiting != "queued" {
		t.Fatal("missing FCM configuration did not preserve the queue")
	}
	w = campaignTestCall(router, "POST", secondPath+"/cancel", map[string]string{"reason": "Testing cancellation"}, true, "super_admin")
	if w.Code != 200 {
		t.Fatalf("cancel %s", w.Body.String())
	}
	third := create("Made-up segment")
	w = campaignTestCall(router, "POST", "/admin/communications/campaigns/"+third+"/send", map[string]any{"version": 1, "purpose": "announcement", "reason": "Invalid audience test"}, true, "super_admin")
	if w.Code != 400 {
		t.Fatal("invalid audience accepted")
	}
	marketingCampaign := create("Drivers")
	w = campaignTestCall(router, "POST", "/admin/communications/campaigns/"+marketingCampaign+"/send", map[string]any{"version": 1, "purpose": "marketing", "reason": "Marketing opt-in test"}, true, "super_admin")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err = fanOutPushBroadcasts(ctx, pool, 100, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var marketingBroadcast string
	if err = pool.QueryRow(ctx, `SELECT id::TEXT FROM admin_push_broadcasts WHERE campaign_id=$1::UUID`, marketingCampaign).Scan(&marketingBroadcast); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_notification_push_deliveries p JOIN driver_notifications n ON n.id=p.notification_id WHERE n.driver_id=$1::UUID AND n.source_event_id=$2`, driver, "broadcast:"+marketingBroadcast).Scan(&count); err != nil || count != 0 {
		t.Fatal("marketing sent without opt-in")
	}
	exec(`INSERT INTO driver_notification_preferences(driver_id,promotions) VALUES($1::UUID,TRUE) ON CONFLICT(driver_id) DO UPDATE SET promotions=TRUE`, driver)
	optedIn := create("Drivers")
	w = campaignTestCall(router, "POST", "/admin/communications/campaigns/"+optedIn+"/send", map[string]any{"version": 1, "purpose": "marketing", "reason": "Marketing opt-in positive test"}, true, "super_admin")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err = fanOutPushBroadcasts(ctx, pool, 100, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var optedInBroadcast string
	if err = pool.QueryRow(ctx, `SELECT id::TEXT FROM admin_push_broadcasts WHERE campaign_id=$1::UUID`, optedIn).Scan(&optedInBroadcast); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_notification_push_deliveries p JOIN driver_notifications n ON n.id=p.notification_id WHERE n.driver_id=$1::UUID AND n.source_event_id=$2`, driver, "broadcast:"+optedInBroadcast).Scan(&count); err != nil || count != 1 {
		t.Fatal("opted-in marketing was not queued")
	}
	exec(`DELETE FROM driver_notification_preferences WHERE driver_id=$1::UUID`, driver)
	var allowed bool
	pool.QueryRow(ctx, `SELECT driver_notification_allowed($1::UUID,'promotion')`, driver).Scan(&allowed)
	if allowed {
		t.Fatal("marketing must default off")
	}
}

// Recipient selection must recheck a user deleted while the broadcast is queued.
// The row lock prevents a stale SELECT snapshot from violating the recipient FK.
func TestBroadcastSkipsConcurrentlyDeletedRecipient(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 6})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	staff, rider, campaign := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1::uuid,$1,'test')`, staff); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles,active) VALUES($1::uuid,$1,$1,ARRAY['rider'],true)`, rider); err != nil {
		t.Fatal(err)
	}
	router := campaignTestRouter(&adminAPI{pool: pool}, staff)
	w := campaignTestCall(router, "POST", "/admin/communications/campaigns", map[string]any{"id": campaign, "name": "Deletion race", "category": "Push", "audience": "Riders", "body": "Test message", "reason": "Testing concurrent deletion"}, true, "super_admin")
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	deleting, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer deleting.Rollback(context.Background())
	if _, err = deleting.Exec(ctx, `DELETE FROM users WHERE id=$1::uuid`, rider); err != nil {
		t.Fatal(err)
	}
	type response struct {
		code int
		body string
	}
	done := make(chan response, 1)
	go func() {
		result := campaignTestCall(router, "POST", "/admin/communications/campaigns/"+campaign+"/send", map[string]any{"version": 1, "purpose": "feedback", "reason": "Testing concurrent deletion"}, true, "super_admin")
		done <- response{result.Code, result.Body.String()}
	}()
	// Wait for the request to reach recipient selection while deletion holds its row.
	for {
		var blocked bool
		err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND query LIKE 'INSERT INTO admin_push_recipients%' AND wait_event_type='Lock')`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case result := <-done:
			t.Fatalf("request did not wait for deletion: %d %s", result.code, result.body)
		case <-ctx.Done():
			t.Fatal("recipient selection did not acquire its row lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = deleting.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.code != 200 {
			t.Fatalf("queue after deletion: %d %s", result.code, result.body)
		}
	case <-ctx.Done():
		t.Fatal("broadcast did not finish after deletion")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM admin_push_recipients WHERE user_id=$1::uuid`, rider).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted recipient count=%d err=%v", count, err)
	}
}
