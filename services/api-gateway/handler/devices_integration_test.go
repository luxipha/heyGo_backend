package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type deviceTestUsers struct{ id string }

func (s deviceTestUsers) UpsertIdentity(context.Context, sharedauth.Identity) (gatewayauth.User, error) {
	return gatewayauth.User{ID: s.id, Roles: []string{"driver"}}, nil
}
func (s deviceTestUsers) AddRole(context.Context, string, string) (gatewayauth.User, error) {
	return gatewayauth.User{}, nil
}

func TestDriverDeviceRegistrationAndOwnerScopedRemoval(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	ownerID, otherID := uuid.NewString(), uuid.NewString()
	for _, id := range []string{ownerID, otherID} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, id, "device-test-"+id, "device-test-"+id); err != nil {
			t.Fatal(err)
		}
	}
	gin.SetMode(gin.TestMode)
	routerFor := func(id string) *gin.Engine {
		middleware := gatewayauth.NewMiddleware(contractTestVerifier{}, deviceTestUsers{id: id})
		router := gin.New()
		authenticated := router.Group("/", middleware.Authenticate)
		registerDeviceRoutes(authenticated, pool)
		return router
	}
	deviceID := uuid.NewString()
	request := httptest.NewRequest(http.MethodPost, "/devices", bytes.NewBufferString(`{"id":"`+deviceID+`","platform":"ios","pushToken":"push-token-test","appVersion":"1.0.0"}`))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	routerFor(ownerID).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", response.Code, response.Body.String())
	}
	var registration struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &registration); err != nil {
		t.Fatal(err)
	}
	if registration.Data.ID != deviceID {
		t.Fatalf("registered ID=%q want %q", registration.Data.ID, deviceID)
	}

	request = httptest.NewRequest(http.MethodDelete, "/devices/"+deviceID, nil)
	request.Header.Set("Authorization", "Bearer test")
	response = httptest.NewRecorder()
	routerFor(otherID).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("non-owner delete status=%d", response.Code)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_devices WHERE id=$1::UUID AND driver_id=$2::UUID`, deviceID, ownerID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("non-owner removed device count=%d err=%v", count, err)
	}

	request = httptest.NewRequest(http.MethodDelete, "/devices/"+deviceID, nil)
	request.Header.Set("Authorization", "Bearer test")
	response = httptest.NewRecorder()
	routerFor(ownerID).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("owner delete status=%d", response.Code)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_devices WHERE id=$1::UUID`, deviceID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("owner delete left device count=%d err=%v", count, err)
	}
}

var _ sharedauth.Verifier = contractTestVerifier{}
