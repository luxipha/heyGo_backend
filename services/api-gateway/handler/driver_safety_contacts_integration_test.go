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

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
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
	if _, err := pool.Exec(ctx, `INSERT INTO driver_profiles(driver_id) VALUES($1::UUID)`, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO driver_identity_profiles(driver_id,next_of_kin,relationship,emergency_number) VALUES($1::UUID,'Onboarding Contact','Mother','08123456789')`, ownerID); err != nil {
		t.Fatal(err)
	}
	fallback := call(ownerID, http.MethodGet, "")
	var fallbackPayload struct {
		Data struct {
			Contact driverSafetyContact `json:"contact"`
		} `json:"data"`
	}
	if err := json.Unmarshal(fallback.Body.Bytes(), &fallbackPayload); err != nil || fallback.Code != 200 || fallbackPayload.Data.Contact.FullName != "Onboarding Contact" || fallbackPayload.Data.Contact.PhoneNumber != "+2348123456789" {
		t.Fatalf("onboarding contact missing: %s", fallback.Body.String())
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
	explicit := call(ownerID, http.MethodGet, "")
	if err := json.Unmarshal(explicit.Body.Bytes(), &fallbackPayload); err != nil || fallbackPayload.Data.Contact.FullName != "Ada Lovelace" {
		t.Fatalf("explicit safety contact must override onboarding: %s", explicit.Body.String())
	}
	added := call(ownerID, http.MethodPost, `{"fullName":"Second Contact","relationship":"Friend","phoneNumber":"08012345679"}`)
	var directory struct {
		Data struct {
			Personal []struct {
				ID   string `json:"id"`
				Name string `json:"fullName"`
			} `json:"personal"`
		} `json:"data"`
	}
	if json.Unmarshal(added.Body.Bytes(), &directory) != nil || added.Code != 200 || len(directory.Data.Personal) != 1 {
		t.Fatalf("additional contact not saved: %s", added.Body.String())
	}
	contactID := directory.Data.Personal[0].ID
	remove := func(owner string) int {
		r := httptest.NewRequest(http.MethodDelete, "/driver/safety-contacts/"+contactID, nil)
		r.Header.Set("Authorization", "Bearer test")
		w := httptest.NewRecorder()
		routerFor(owner).ServeHTTP(w, r)
		return w.Code
	}
	if remove(otherID) != 404 {
		t.Fatal("other driver could remove contact")
	}
	if remove(ownerID) != 204 {
		t.Fatal("owner could not remove contact")
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
