package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
)

func TestRideAgentLocalTransportPreservesInternalAuthentication(t *testing.T) {
	token := strings.Repeat("a", 48)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/resolve" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Serverless-Authorization") != "" {
			t.Error("local Ride Agent authentication changed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"schemaVersion":1,"state":"unavailable","confirmationRequired":true,"message":"Unavailable"}`))
	}))
	defer server.Close()
	t.Setenv("RIDE_AGENT_URL", server.URL)
	t.Setenv("RIDE_AGENT_INTERNAL_TOKEN", token)
	t.Setenv("RIDE_AGENT_AUDIENCE", "")
	if _, err := callRideAgent(context.Background(), agent.Request{SchemaVersion: 1, Message: "Airport"}); err != nil {
		t.Fatal(err)
	}
}

func TestRideAgentRejectsInvalidCloudRunAudienceBeforeSending(t *testing.T) {
	t.Setenv("RIDE_AGENT_URL", "http://127.0.0.1:1")
	t.Setenv("RIDE_AGENT_INTERNAL_TOKEN", strings.Repeat("a", 48))
	t.Setenv("RIDE_AGENT_AUDIENCE", "http://untrusted.invalid")
	if _, err := callRideAgent(context.Background(), agent.Request{SchemaVersion: 1}); err == nil || err.Error() != "ride agent identity unavailable" {
		t.Fatalf("invalid identity configuration was not rejected: %v", err)
	}
}
