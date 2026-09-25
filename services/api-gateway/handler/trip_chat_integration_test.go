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

	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type tripChatUsers struct{ id string }

func (s tripChatUsers) UpsertIdentity(context.Context, sharedauth.Identity) (gatewayauth.User, error) {
	return gatewayauth.User{ID: s.id, Roles: []string{"rider", "driver"}}, nil
}
func (s tripChatUsers) AddRole(context.Context, string, string) (gatewayauth.User, error) {
	return gatewayauth.User{ID: s.id, Roles: []string{"rider", "driver"}}, nil
}

func TestTripChatLifecycleAndParticipants(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	riderID, driverID, outsiderID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, item := range []struct{ id, role string }{{riderID, "rider"}, {driverID, "driver"}, {outsiderID, "rider"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY[$4]::TEXT[])`, item.id, "trip-chat-test-"+item.id, "trip-chat-test-"+item.id, item.role); err != nil {
			t.Fatal(err)
		}
	}
	tripID, fareID := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO ride_fares(id,rider_id,package_slug,total_fare_minor,route) VALUES($1::UUID,$2::UUID,'sedan',500000,'{}'::JSONB)`, fareID, riderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trips(id,rider_id,ride_fare_id,assigned_driver_id,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,'accepted')`, tripID, riderID, fareID, driverID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM trips WHERE id=$1::UUID`, tripID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM ride_fares WHERE id=$1::UUID`, fareID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1::UUID,$2::UUID,$3::UUID)`, riderID, driverID, outsiderID)
	})

	gin.SetMode(gin.TestMode)
	routerFor := func(id string) *gin.Engine {
		router := gin.New()
		middleware := gatewayauth.NewMiddleware(contractTestVerifier{}, tripChatUsers{id: id})
		authenticated := router.Group("/", middleware.Authenticate)
		registerTripChatRoutes(authenticated, pool)
		return router
	}
	request := func(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	riderRouter, driverRouter, outsiderRouter := routerFor(riderID), routerFor(driverID), routerFor(outsiderID)
	path := "/trips/" + tripID + "/messages"
	messageID := uuid.NewString()
	body := `{"clientMessageId":"` + messageID + `","body":"Hello driver"}`
	response := request(riderRouter, http.MethodPost, path, body)
	if response.Code != http.StatusCreated {
		t.Fatalf("send status=%d body=%s", response.Code, response.Body.String())
	}
	if replay := request(riderRouter, http.MethodPost, path, body); replay.Code != http.StatusCreated {
		t.Fatalf("duplicate message status=%d body=%s", replay.Code, replay.Body.String())
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM trip_chat_messages WHERE trip_id=$1::UUID`, tripID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate send inserted %d messages, err=%v", count, err)
	}
	var liveEvents int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_events WHERE recipient_id=$1::UUID AND type='trip.message.created'`, driverID).Scan(&liveEvents); err != nil || liveEvents != 1 {
		t.Fatalf("message live events=%d err=%v", liveEvents, err)
	}
	if forbidden := request(outsiderRouter, http.MethodGet, path, ""); forbidden.Code != http.StatusForbidden {
		t.Fatalf("outsider read status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
	var sent struct {
		Data struct {
			Message tripChatMessage `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &sent); err != nil {
		t.Fatal(err)
	}
	readBody := `{"throughMessageId":"` + sent.Data.Message.ID + `"}`
	readResponse := request(driverRouter, http.MethodPost, path+"/read", readBody)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("mark read status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}
	var receiptEvents int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_events WHERE recipient_id=$1::UUID AND type='trip.message.read'`, riderID).Scan(&receiptEvents); err != nil || receiptEvents != 1 {
		t.Fatalf("read receipt events=%d err=%v", receiptEvents, err)
	}
	if history := request(driverRouter, http.MethodGet, path, ""); history.Code != http.StatusOK || !strings.Contains(history.Body.String(), "Hello driver") {
		t.Fatalf("history status=%d body=%s", history.Code, history.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE trips SET status='completed',completed_at=NOW(),updated_at=NOW() WHERE id=$1::UUID`, tripID); err != nil {
		t.Fatal(err)
	}
	if afterEnd := request(riderRouter, http.MethodPost, path, `{"clientMessageId":"`+uuid.NewString()+`","body":"after trip"}`); afterEnd.Code != http.StatusConflict {
		t.Fatalf("post-trip send status=%d body=%s", afterEnd.Code, afterEnd.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE trips SET completed_at=NOW()-INTERVAL '31 days' WHERE id=$1::UUID`, tripID); err != nil {
		t.Fatal(err)
	}
	if expired := request(riderRouter, http.MethodGet, path, ""); expired.Code != http.StatusGone {
		t.Fatalf("expired history status=%d body=%s", expired.Code, expired.Body.String())
	}
}
