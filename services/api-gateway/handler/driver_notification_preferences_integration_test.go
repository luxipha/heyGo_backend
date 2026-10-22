package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDriverNotificationPreferencesDeliveryAndOwnership(t *testing.T) {
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
	owner, other, device := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, other} {
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$1,$1,ARRAY['driver'])`, id); err != nil {
			t.Fatal(err)
		}
	}
	defer pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::UUID[])`, []string{owner, other})
	if _, err = pool.Exec(ctx, `INSERT INTO driver_devices(id,driver_id,platform,push_token) VALUES($1::UUID,$2::UUID,'android',$1)`, device, owner); err != nil {
		t.Fatal(err)
	}
	call := func(who, method, body string) *httptest.ResponseRecorder {
		r := gin.New()
		auth := gatewayauth.NewMiddleware(contractTestVerifier{}, safetyContactTestUsers{id: who})
		registerDriverRoutes(r.Group("/", auth.Authenticate), pool, nil)
		req := httptest.NewRequest(method, "/driver/notification-preferences", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("%d want %d: %s", w.Code, status, w.Body.String())
		}
	}
	emit := func(kind string) {
		t.Helper()
		tx, e := pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if _, e = messaging.AppendEventTx(ctx, tx, owner, uuid.NewString(), kind, json.RawMessage(`{}`)); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	count := func(query string, want int) {
		t.Helper()
		var got int
		if e := pool.QueryRow(ctx, query, owner).Scan(&got); e != nil {
			t.Fatal(e)
		}
		if got != want {
			t.Fatalf("count %d want %d: %s", got, want, query)
		}
	}
	check(call(owner, "PATCH", `{}`), 400)
	check(call(owner, "PATCH", `{"rideRequests":"false"}`), 400)
	wDocs := call(owner, "PATCH", `{"documentReminders":false}`)
	check(wDocs, 200)
	if !strings.Contains(wDocs.Body.String(), `"documentReminders":false`) || !strings.Contains(wDocs.Body.String(), `"rideRequests":true`) {
		t.Fatal("document preference did not save independently")
	}
	emit("driver.cmd.trip_request")
	job, e := claimPushDeliveryForDriver(ctx, pool, owner)
	if e != nil || job == nil {
		t.Fatalf("claim %v %v", job, e)
	}
	if !driverPushStillAllowed(ctx, pool, *job) {
		t.Fatal("default preference should allow delivery")
	}
	check(call(owner, "PATCH", `{"rideRequests":false}`), 200)
	if driverPushStillAllowed(ctx, pool, *job) {
		t.Fatal("claimed delivery must honor disabled preference")
	}
	finishPushDelivery(ctx, pool, *job, nil)
	count(`SELECT COUNT(*) FROM driver_notification_push_deliveries d JOIN driver_notifications n ON n.id=d.notification_id WHERE n.driver_id=$1::UUID AND d.status='dead'`, 1)
	emit("driver.cmd.trip_request")
	emit("trip.event.cancelled")
	emit("trip.event.settlement_updated")
	count(`SELECT COUNT(*) FROM driver_notification_push_deliveries d JOIN driver_notifications n ON n.id=d.notification_id WHERE n.driver_id=$1::UUID AND d.status='pending'`, 2)
	check(call(owner, "PATCH", `{"tripUpdates":false,"earnings":false}`), 200)
	emit("trip.event.cancelled")
	emit("trip.event.settlement_updated")
	emit("driver.operating_balance.updated")
	count(`SELECT COUNT(*) FROM driver_notifications WHERE driver_id=$1::UUID`, 7)
	count(`SELECT COUNT(*) FROM user_events WHERE recipient_id=$1::UUID AND type='trip.event.cancelled'`, 2)
	count(`SELECT COUNT(*) FROM driver_notification_push_deliveries d JOIN driver_notifications n ON n.id=d.notification_id WHERE n.driver_id=$1::UUID AND d.status='pending'`, 0)
	w := call(other, "GET", "")
	check(w, 200)
	if !strings.Contains(w.Body.String(), `"rideRequests":true`) || !strings.Contains(w.Body.String(), `"earnings":true`) {
		t.Fatal("preference leaked across users")
	}
	check(call(owner, "PATCH", `{"rideRequests":true}`), 200)
	emit("driver.cmd.trip_request")
	count(`SELECT COUNT(*) FROM driver_notification_push_deliveries d JOIN driver_notifications n ON n.id=d.notification_id WHERE n.driver_id=$1::UUID AND d.status='pending'`, 1)
}
