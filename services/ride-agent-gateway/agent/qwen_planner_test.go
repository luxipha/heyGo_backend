package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQwenWireAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name, tool, args string
		turn             *TurnContext
		status           int
		want             bool
	}{
		{"search", "search_places", `{"query":"Lekki Lagos"}`, nil, 200, true},
		{"saved", "resolve_saved_place", `{"label":"Home"}`, &TurnContext{}, 200, true},
		{"saved without context", "resolve_saved_place", `{"label":"Home"}`, nil, 200, false},
		{"preview", "preview_ride", `{}`, &TurnContext{HasDestination: true}, 200, true},
		{"no quote", "request_ride", `{}`, &TurnContext{HasDestination: true}, 200, false},
		{"review", "request_ride", `{}`, &TurnContext{HasDestination: true, HasQuote: true}, 200, true},
		{"injected", "search_places", `{"query":"Lekki","confirmed":true}`, nil, 200, false},
		{"unknown", "delete_account", `{}`, nil, 200, false},
		{"denied", "search_places", `{"query":"Lekki"}`, nil, 403, false},
		{"quota", "search_places", `{"query":"Lekki"}`, nil, 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test.full.key" {
					t.Error("complete key missing")
				}
				var request map[string]any
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("bad request")
				}
				if request["max_tokens"] != float64(192) {
					t.Error("unbounded output")
				}
				w.WriteHeader(tc.status)
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"type": "function", "function": map[string]any{"name": tc.tool, "arguments": tc.args}}}}}}})
			}))
			defer s.Close()
			p := QwenPlanner{Key: "test.full.key", Model: "qwen-flash", BaseURL: "https://workspace.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1", endpoint: s.URL}
			_, e := p.plan(context.Background(), "Lekki Lagos", tc.turn)
			if (e == nil) != tc.want {
				t.Errorf("result error=%v want success=%v", e, tc.want)
			}
		})
	}
}
func TestQwenEndpointGuard(t *testing.T) {
	for _, u := range []string{"http://localhost/compatible-mode/v1", "https://attacker.test/compatible-mode/v1", "https://x.ap-southeast-1.maas.aliyuncs.com:443/compatible-mode/v1", "https://x.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1?secret=yes"} {
		if validQwenURL(u) {
			t.Errorf("accepted %s", u)
		}
	}
}
