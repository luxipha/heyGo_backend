package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestDriverNotificationInboxReadStateAndLiveEvent(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, id, "notification-test-"+id, "notification-test-"+id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::UUID`, id) })
	deviceID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO driver_devices(id,driver_id,platform,push_token) VALUES($1::UUID,$2::UUID,'android',$3)`, deviceID, id, "notif-device-"+deviceID); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	events := []struct {
		kind string
		data map[string]any
	}{
		{"driver.document.updated", map[string]any{"type": "driver_license", "status": "approved"}},
		{"trip.event.settlement_updated", map[string]any{"status": "confirmed", "expectedAmountKobo": 500000}},
		{"driver.operating_balance.updated", map[string]any{"kind": "topup", "deltaKobo": 500000}},
	}
	for _, item := range events {
		payload, _ := json.Marshal(item.data)
		if _, err := messaging.AppendEventTx(ctx, tx, id, uuid.NewString(), item.kind, payload); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var eventCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_events WHERE recipient_id=$1::UUID AND type='notification.created'`, id).Scan(&eventCount); err != nil || eventCount != 3 {
		t.Fatalf("live notification events=%d err=%v", eventCount, err)
	}
	job, err := claimPushDeliveryForDriver(ctx, pool, id)
	if err != nil || job == nil || job.DeviceID != deviceID {
		t.Fatalf("claim push job=%+v err=%v", job, err)
	}
	finishPushDelivery(ctx, pool, *job, nil)
	var sent int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_notification_push_deliveries WHERE device_id=$1::UUID AND status='sent'`, deviceID).Scan(&sent); err != nil || sent != 1 {
		t.Fatalf("sent push jobs=%d err=%v", sent, err)
	}
	job, err = claimPushDeliveryForDriver(ctx, pool, id)
	if err != nil || job == nil {
		t.Fatalf("claim retryable push job=%+v err=%v", job, err)
	}
	finishPushDelivery(ctx, pool, *job, errors.New("temporary FCM failure"))
	var pending, attempts int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*),COALESCE(MAX(attempts),0) FROM driver_notification_push_deliveries WHERE device_id=$1::UUID AND status='pending'`, deviceID).Scan(&pending, &attempts); err != nil || pending != 2 || attempts != 1 {
		t.Fatalf("retryable pushes pending=%d attempts=%d err=%v", pending, attempts, err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	middleware := gatewayauth.NewMiddleware(contractTestVerifier{}, deviceTestUsers{id: id})
	authenticated := router.Group("/", middleware.Authenticate)
	driver := authenticated.Group("/driver")
	registerDriverNotificationRoutes(driver, &driverAPI{pool: pool})
	request := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer test")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	response := request(http.MethodGet, "/driver/notifications")
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	var page struct {
		Data struct {
			Items       []driverNotification `json:"items"`
			UnreadCount int                  `json:"unreadCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Items) != 3 || page.Data.UnreadCount != 3 {
		t.Fatalf("inbox items=%d unread=%d", len(page.Data.Items), page.Data.UnreadCount)
	}
	response = request(http.MethodPatch, "/driver/notifications/"+page.Data.Items[0].ID)
	if response.Code != http.StatusOK {
		t.Fatalf("mark one read status=%d body=%s", response.Code, response.Body.String())
	}
	response = request(http.MethodPost, "/driver/notifications/read-all")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"updatedCount":2`) {
		t.Fatalf("mark all read status=%d body=%s", response.Code, response.Body.String())
	}
	response = request(http.MethodGet, "/driver/notifications?cursor=invalid")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status=%d", response.Code)
	}
}
