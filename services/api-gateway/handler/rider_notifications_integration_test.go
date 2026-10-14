package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRiderNotificationsOwnershipPreferencesAndDeliveryRecovery(t *testing.T) {
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
	owner, other, driver, device, session, fare, trip := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []string{owner, other, driver} {
		exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$1,$1,ARRAY['rider'])`, id)
	}
	exec(`UPDATE users SET roles=ARRAY['driver'] WHERE id=$1::UUID`, driver)
	exec(`INSERT INTO user_sessions(id,user_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at) VALUES($1::UUID,$2::UUID,$3,$4,NOW()+INTERVAL '15 minutes',NOW()+INTERVAL '1 day')`, session, owner, gatewayauth.TokenHash("test-"+owner), "refresh-"+session)
	exec(`INSERT INTO user_sessions(id,user_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at) VALUES($1::UUID,$2::UUID,$3,$4,NOW()+INTERVAL '15 minutes',NOW()+INTERVAL '1 day')`, uuid.NewString(), other, gatewayauth.TokenHash("test-"+other), "refresh-"+other)
	exec(`INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route,pickup,destination) VALUES($1::UUID,$2::UUID,'sedan',10000,'{}',ST_SetSRID(ST_MakePoint(3.9,6.9),4326)::geography,ST_SetSRID(ST_MakePoint(3.91,6.91),4326)::geography)`, fare, owner)
	exec(`INSERT INTO trips(id,rider_id,ride_fare_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,'pending')`, trip, owner, fare)
	defer func() {
		for _, id := range []string{owner, other, driver} {
			_, _ = pool.Exec(ctx, `DELETE FROM user_events WHERE recipient_id=$1::UUID`, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM trips WHERE id=$1::UUID`, trip)
		_, _ = pool.Exec(ctx, `DELETE FROM ride_fares WHERE id=$1::UUID`, fare)
		for _, id := range []string{owner, other, driver} {
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1::UUID`, id)
		}
	}()
	r := gin.New()
	g := r.Group("", func(c *gin.Context) {
		id := c.GetHeader("X-Owner")
		role := "rider"
		if id == driver {
			role = "driver"
		}
		c.Set("authenticated-user", gatewayauth.User{ID: id, Roles: []string{role}})
	})
	registerRiderAccountRoutes(g, pool, nil)
	call := func(method, path, who, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Owner", who)
		req.Header.Set("Authorization", "Bearer test-"+who)
		r.ServeHTTP(w, req)
		return w
	}
	if got := call("GET", "/rider/notifications", driver, ""); got.Code != 403 {
		t.Fatal(got.Body.String())
	}
	if got := call("GET", "/rider/notification-preferences", owner, ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"promotions":false`) {
		t.Fatal(got.Body.String())
	}
	register := func(token string) *httptest.ResponseRecorder {
		return call("POST", "/rider/devices", owner, `{"id":"`+device+`","platform":"android","pushToken":"`+token+`"}`)
	}
	if got := register("old-token-" + device); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if got := call("POST", "/rider/devices", other, `{"id":"`+device+`","platform":"ios","pushToken":"foreign"}`); got.Code != 409 {
		t.Fatal("foreign registration ownership was not enforced: " + got.Body.String())
	}
	appendEvent := func(source string) {
		t.Helper()
		_, e := messaging.NewEventStore(pool).Append(ctx, owner, source, contracts.TripEventDriverAssigned, json.RawMessage(`{"id":"`+trip+`","riderID":"`+owner+`","driver":{"phone":"secret","latitude":6.4}}`))
		if e != nil {
			t.Fatal(e)
		}
	}
	source := "rider-notif-" + uuid.NewString()
	appendEvent(source)
	failures := make(chan error, 8)
	for range 8 {
		go func() {
			_, e := messaging.NewEventStore(pool).Append(ctx, owner, source, contracts.TripEventDriverAssigned, json.RawMessage(`{"id":"`+trip+`"}`))
			failures <- e
		}()
	}
	for range 8 {
		if e := <-failures; e != nil {
			t.Fatal(e)
		}
	}
	var count int
	execCount := func(q string, args ...any) int {
		t.Helper()
		if e := pool.QueryRow(ctx, q, args...).Scan(&count); e != nil {
			t.Fatal(e)
		}
		return count
	}
	if execCount(`SELECT COUNT(*) FROM rider_notifications WHERE rider_id=$1::UUID`, owner) != 1 {
		t.Fatal("duplicate notification")
	}
	if execCount(`SELECT COUNT(*) FROM rider_notification_push_deliveries p JOIN rider_devices d ON d.id=p.device_id WHERE d.rider_id=$1::UUID`, owner) != 1 {
		t.Fatal("duplicate delivery")
	}
	var notificationID string
	if err = pool.QueryRow(ctx, `SELECT id::TEXT FROM rider_notifications WHERE rider_id=$1::UUID`, owner).Scan(&notificationID); err != nil {
		t.Fatal(err)
	}
	detail := call("GET", "/rider/notifications/"+notificationID, owner, "")
	if detail.Code != 200 || strings.Contains(detail.Body.String(), "secret") || strings.Contains(detail.Body.String(), "latitude") {
		t.Fatal(detail.Body.String())
	}
	if got := call("GET", "/rider/notifications/"+notificationID, other, ""); got.Code != 404 {
		t.Fatal(got.Body.String())
	}
	if got := call("PATCH", "/rider/notifications/"+notificationID, other, ""); got.Code != 404 {
		t.Fatal(got.Body.String())
	}
	job, err := claimRiderPushDelivery(ctx, pool)
	if err != nil || job == nil {
		t.Fatalf("claim: %v %v", job, err)
	}
	if !riderPushStillAllowed(ctx, pool, *job) {
		t.Fatal("owned job not eligible")
	}
	if got := register("new-token-" + device); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if riderPushStillAllowed(ctx, pool, *job) {
		t.Fatal("old token still eligible")
	}
	finishRiderPushDelivery(ctx, pool, *job, errFCMUnregistered)
	var enabled bool
	if err = pool.QueryRow(ctx, `SELECT enabled FROM rider_devices WHERE id=$1::UUID`, device).Scan(&enabled); err != nil || !enabled {
		t.Fatal("old response disabled rotated token")
	}
	appendEvent(source + "-retry")
	job, err = claimRiderPushDelivery(ctx, pool)
	if err != nil || job == nil {
		t.Fatalf("retry claim %v", err)
	}
	finishRiderPushDelivery(ctx, pool, *job, errors.New("controlled transient failure"))
	exec(`UPDATE rider_notification_push_deliveries SET next_attempt_at=NOW() WHERE id=$1`, job.ID)
	job, err = claimRiderPushDelivery(ctx, pool)
	if err != nil || job == nil || job.Attempts != 2 {
		t.Fatalf("durable retry %+v %v", job, err)
	}
	// An expired lease permits recovery; stale worker completion cannot overwrite the new attempt.
	stale := *job
	exec(`UPDATE rider_notification_push_deliveries SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, job.ID)
	job, err = claimRiderPushDelivery(ctx, pool)
	if err != nil || job == nil || job.Attempts != 3 {
		t.Fatalf("lease recovery %+v %v", job, err)
	}
	finishRiderPushDelivery(ctx, pool, stale, nil)
	if execCount(`SELECT COUNT(*) FROM rider_notification_push_deliveries WHERE id=$1 AND status='processing'`, job.ID) != 1 {
		t.Fatal("stale worker changed state")
	}
	finishRiderPushDelivery(ctx, pool, *job, nil)
	if got := call("PATCH", "/rider/notification-preferences", owner, `{"rideUpdates":false}`); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	appendEvent(source + "-disabled")
	if got := call("GET", "/rider/notifications", owner, ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"unreadCount":3`) {
		t.Fatal(got.Body.String())
	}
	if execCount(`SELECT COUNT(*) FROM rider_notification_push_deliveries p JOIN rider_devices d ON d.id=p.device_id WHERE d.rider_id=$1::UUID`, owner) != 2 {
		t.Fatal("disabled preference still queued push")
	}
	call("PATCH", "/rider/notifications/"+notificationID, owner, "")
	call("POST", "/rider/notifications/read-all", other, "")
	if got := call("GET", "/rider/notifications", owner, ""); !strings.Contains(got.Body.String(), `"unreadCount":2`) {
		t.Fatal(got.Body.String())
	}
	call("POST", "/rider/notifications/read-all", owner, "")
	if got := call("GET", "/rider/notifications", owner, ""); !strings.Contains(got.Body.String(), `"unreadCount":0`) {
		t.Fatal(got.Body.String())
	}
	for i := 0; i < 31; i++ {
		appendEvent(source + uuid.NewString())
	}
	page := call("GET", "/rider/notifications", owner, "")
	var response struct {
		Data struct {
			Items []json.RawMessage `json:"items"`
		}
		Meta struct {
			Cursor *string `json:"nextCursor"`
		}
	}
	if json.Unmarshal(page.Body.Bytes(), &response) != nil || len(response.Data.Items) != 30 || response.Meta.Cursor == nil {
		t.Fatal(page.Body.String())
	}
	if got := call("GET", "/rider/notifications?cursor="+*response.Meta.Cursor, owner, ""); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if got := call("GET", "/rider/notifications?cursor=bad", owner, ""); got.Code != 400 {
		t.Fatal(got.Body.String())
	}
	call("PATCH", "/rider/notification-preferences", owner, `{"rideUpdates":true}`)
	appendEvent(source + "-revoked")
	exec(`UPDATE user_sessions SET revoked_at=NOW() WHERE id=$1::UUID`, session)
	job, err = claimRiderPushDelivery(ctx, pool)
	if err != nil || job != nil {
		t.Fatalf("revoked session received job %+v %v", job, err)
	}
	call("DELETE", "/rider/devices/"+device, other, "")
	if execCount(`SELECT COUNT(*) FROM rider_devices WHERE id=$1::UUID AND enabled`, device) != 1 {
		t.Fatal("foreign deletion changed device")
	}
	newDevice := uuid.NewString()
	if got := call("POST", "/rider/devices", other, `{"id":"`+newDevice+`","platform":"android","pushToken":"new-token-`+device+`"}`); got.Code != 200 {
		t.Fatal("revoked session token was not safely released: " + got.Body.String())
	}
	call("DELETE", "/rider/devices/"+device, owner, "")
	if execCount(`SELECT COUNT(*) FROM rider_devices WHERE id=$1::UUID AND enabled`, device) != 0 {
		t.Fatal("removal did not persist")
	}
	before := execCount(`SELECT COUNT(*) FROM rider_notifications WHERE rider_id=$1::UUID`, owner)
	deletion, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer deletion.Rollback(ctx)
	if _, err = deletion.Exec(ctx, `UPDATE users SET active=FALSE WHERE id=$1::UUID`, owner); err != nil {
		t.Fatal(err)
	}
	appended := make(chan error, 1)
	go func() {
		_, e := messaging.NewEventStore(pool).Append(ctx, owner, source+"-deactivation-race", contracts.TripEventDriverAssigned, json.RawMessage(`{"id":"`+trip+`"}`))
		appended <- e
	}()
	select {
	case e := <-appended:
		t.Fatalf("fan-out passed an uncommitted account lock: %v", e)
	case <-time.After(100 * time.Millisecond):
	}
	if err = deletion.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-appended:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fan-out did not recover after account lock")
	}
	if execCount(`SELECT COUNT(*) FROM rider_notifications WHERE rider_id=$1::UUID`, owner) != before {
		t.Fatal("deactivated account received a new notification")
	}

}
