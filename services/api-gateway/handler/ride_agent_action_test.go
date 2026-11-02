package handler

import (
	"context"
	"encoding/json"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentActionProposalsRequireContextAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		action string
		turn   *agent.TurnContext
		valid  bool
	}{
		{"preview_ride", nil, false},
		{"preview_ride", &agent.TurnContext{HasDestination: true}, true},
		{"request_ride", &agent.TurnContext{HasDestination: true}, false},
		{"request_ride", &agent.TurnContext{HasDestination: true, HasQuote: true}, true},
		{"charge_wallet", &agent.TurnContext{HasDestination: true, HasQuote: true}, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(agent.Response{SchemaVersion: 1, State: "action", Action: tc.action, ConfirmationRequired: true, Candidates: []agent.Place{}})
		}))
		t.Setenv("RIDE_AGENT_URL", server.URL)
		t.Setenv("RIDE_AGENT_INTERNAL_TOKEN", strings.Repeat("x", 32))
		t.Setenv("RIDE_AGENT_AUDIENCE", "")
		_, err := callRideAgent(context.Background(), agent.Request{SchemaVersion: 1, Message: "review ride", Context: tc.turn})
		server.Close()
		if (err == nil) != tc.valid {
			t.Fatalf("action=%s valid=%v err=%v", tc.action, tc.valid, err)
		}
	}
}
