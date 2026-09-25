package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	gatewayauth "github.com/cprakhar/uber-clone/services/api-gateway/auth"
	sharedauth "github.com/cprakhar/uber-clone/shared/auth"
	shareddb "github.com/cprakhar/uber-clone/shared/db"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
	"github.com/google/uuid"
)

type contractTestVerifier struct{}

func (contractTestVerifier) Verify(context.Context, string) (sharedauth.Identity, error) {
	return sharedauth.Identity{Subject: "test", HumanID: "test", Verified: true}, nil
}

type contractTestUsers struct{ id string }

func (s contractTestUsers) UpsertIdentity(context.Context, sharedauth.Identity) (gatewayauth.User, error) {
	return gatewayauth.User{ID: s.id, Roles: []string{"rider"}}, nil
}
func (s contractTestUsers) AddRole(context.Context, string, string) (gatewayauth.User, error) {
	return gatewayauth.User{}, nil
}

func TestUnusedSingularLifecycleAliasesAreNotRegistered(t *testing.T) {
	users := contractTestUsers{id: uuid.NewString()}
	router := NewHTTPHandler(nil, nil, gatewayauth.NewMiddleware(contractTestVerifier{}, users), users, nil, nil, nil, CasperIDOAuthConfig{}, func(context.Context) error { return nil })
	routes := map[string]bool{}
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, path := range []string{"/trip/:tripID/complete", "/trip/:tripID/cancel", "/trip/:tripID/rating"} {
		if routes["POST "+path] {
			t.Fatalf("unused singular route still registered: %s", path)
		}
	}
	for _, path := range []string{"/trips/:tripID/complete", "/trips/:tripID/cancel", "/trips/:tripID/rating", "/trip/preview", "/trip/start"} {
		if !routes["POST "+path] {
			t.Fatalf("active route missing: %s", path)
		}
	}
}

func TestRiderTripStartUsesErrorEnvelopeAndIdempotency(t *testing.T) {
	t.Setenv("LOG_ROTATE", "false")
	if _, err := logs.Init("api-gateway-contract-integration-test"); err != nil {
		t.Fatal(err)
	}
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1::UUID,$2,$3,ARRAY['driver'])`, id, "contract-"+id, "contract-"+id); err != nil {
		t.Fatal(err)
	}
	users := contractTestUsers{id: id}
	router := NewHTTPHandler(nil, nil, gatewayauth.NewMiddleware(contractTestVerifier{}, users), users, pool, nil, []string{"https://app.heygo.test"}, CasperIDOAuthConfig{}, func(context.Context) error { return nil })
	request := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/trip/start", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		req.Header.Set("Idempotency-Key", "rider-start-"+id)
		router.ServeHTTP(recorder, req)
		return recorder
	}
	first := request(`{"rideFareID":"not-a-uuid"}`)
	second := request(`{"rideFareID":"not-a-uuid"}`)
	var firstBody, replayBody map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &replayBody); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	firstError, _ := firstBody["error"].(map[string]any)
	if first.Code != http.StatusBadRequest || second.Code != first.Code || !strings.Contains(first.Body.String(), `"code":"invalid_fare_id"`) || firstError["code"] != "invalid_fare_id" || !reflect.DeepEqual(firstBody, replayBody) {
		t.Fatalf("legacy replay: first=%d %s second=%d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	conflict := request(`{"different":true}`)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), `"code":"idempotency_key_reused"`) {
		t.Fatalf("legacy key reuse: %d %s", conflict.Code, conflict.Body.String())
	}
}
