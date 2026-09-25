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

	gatewayauth "github.com/cprakhar/uber-clone/services/api-gateway/auth"
	sharedauth "github.com/cprakhar/uber-clone/shared/auth"
	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type safetyContactTestUsers struct{ id string }

func (s safetyContactTestUsers) UpsertIdentity(context.Context, sharedauth.Identity) (gatewayauth.User, error) {
	return gatewayauth.User{ID: s.id, Roles: []string{"driver"}}, nil
}
func (s safetyContactTestUsers) AddRole(context.Context, string, string) (gatewayauth.User, error) {
	return gatewayauth.User{}, nil
}

func TestDriverSafetyContactReadAndUpdateAreOwnerScoped(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: databaseURL, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	ownerID, otherID := uuid.NewString(), uuid.NewString()
	for _, id := range []string{ownerID, otherID} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, id, "safety-contact:"+id, "safety-contact-human:"+id); err != nil {
			t.Fatal(err)
		}
	}
	gin.SetMode(gin.TestMode)
	routerFor := func(id string) *gin.Engine {
		middleware := gatewayauth.NewMiddleware(contractTestVerifier{}, safetyContactTestUsers{id: id})
		router := gin.New()
		authenticated := router.Group("/", middleware.Authenticate)
		registerDriverRoutes(authenticated, pool, nil)
		return router
	}
	call := func(id, method, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/driver/safety-contacts", bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer test")
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		routerFor(id).ServeHTTP(response, request)
		return response
	}
	if response := call(ownerID, http.MethodGet, ""); response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) {
		t.Fatalf("empty contact read status=%d body=%s", response.Code, response.Body.String())
	}
	invalid := call(ownerID, http.MethodPut, `{"fullName":"Next of Kin","relationship":"Sibling","phoneNumber":"bad"}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid contact status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	saved := call(ownerID, http.MethodPut, `{"fullName":"  Ada Lovelace ","relationship":" Sister ","phoneNumber":"0801 234 5678"}`)
	if saved.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	var payload struct {
		Data struct {
			Contact driverSafetyContact `json:"contact"`
		} `json:"data"`
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Contact.FullName != "Ada Lovelace" || payload.Data.Contact.Relationship != "Sister" || payload.Data.Contact.PhoneNumber != "+2348012345678" {
		t.Fatalf("saved contact = %+v", payload.Data.Contact)
	}
	other := call(otherID, http.MethodGet, "")
	if other.Code != http.StatusOK {
		t.Fatalf("other driver read status=%d body=%s", other.Code, other.Body.String())
	}
	var otherPayload struct {
		Data struct {
			Contact *driverSafetyContact `json:"contact"`
		} `json:"data"`
	}
	if err := json.Unmarshal(other.Body.Bytes(), &otherPayload); err != nil || otherPayload.Data.Contact != nil {
		t.Fatalf("other driver received contact=%+v err=%v", otherPayload.Data.Contact, err)
	}
}
