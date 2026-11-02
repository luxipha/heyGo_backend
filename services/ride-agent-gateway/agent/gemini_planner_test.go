package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiPlannerWireContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("x-goog-api-key") != "test.key.with.dots" || r.URL.RawQuery != "" {
			t.Error("credential must be complete and header-only")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("invalid body")
		}
		cfg := body["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)
		if cfg["mode"] != "ANY" || cfg["allowedFunctionNames"].([]any)[0] != "search_places" {
			t.Error("tool allowlist missing")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"search_places","args":{"query":"Lekki Conservation Centre Lagos"}}}]}}]}`))
	}))
	defer server.Close()
	plan, err := (GeminiPlanner{Key: "test.key.with.dots", Model: "gemini-2.5-flash-lite", endpoint: server.URL}).Plan(context.Background(), "take me to Lekki")
	if err != nil || plan.Tool != "search_places" || plan.Query != "Lekki Conservation Centre Lagos" {
		t.Fatalf("invalid normalized plan: %#v %v", plan, err)
	}
}
func TestGeminiRejectsUnsafeAndAmbiguousProposals(t *testing.T) {
	for _, response := range []string{
		`{"candidates":[{"content":{"parts":[{"text":"I booked it"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"dispatch","args":{"query":"Lekki"}}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"search_places","args":{"query":"Lekki","fare":100}}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"search_places","args":{"query":"wallet balance"}}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"search_places","args":{"query":"Lekki"}}},{"functionCall":{"name":"search_places","args":{"query":"Ikeja"}}}]}}]}`,
		strings.Repeat("x", 32769),
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(response)) }))
		_, err := (GeminiPlanner{Key: "test", Model: "gemini-2.5-flash-lite", endpoint: server.URL}).Plan(context.Background(), "Lekki")
		server.Close()
		if err == nil {
			t.Error("unsafe/unbounded proposal accepted")
		}
	}
}
func TestGeminiFailureRetainsDeterministicPlaceSearch(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusForbidden, http.StatusFound} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://example.invalid")
			w.WriteHeader(status)
			w.Write([]byte("private provider diagnostic"))
		}))
		places := &fakePlaces{places: []Place{fixture()}}
		gateway := Gateway{Places: places, LLMEnabled: true, Planner: GeminiPlanner{Key: "private-key", Model: "gemini-2.5-flash-lite", endpoint: server.URL}}
		reply, err := gateway.Resolve(context.Background(), Request{SchemaVersion: 1, Message: "take me to Lekki"})
		server.Close()
		if err != nil || reply.Planner != "deterministic" || reply.State != "resolved" || places.query != "Lekki" {
			t.Fatalf("fallback failed: %#v %v", reply, err)
		}
	}
}
func TestConfiguredGeminiRejectsMissingKeyAndModelPathInjection(t *testing.T) {
	t.Setenv("RIDE_AGENT_PLANNER_PROVIDER", "gemini")
	t.Setenv("GEMINI_API_KEY", "")
	if _, err := ConfiguredPlanner(); err == nil {
		t.Error("missing key accepted")
	}
	t.Setenv("GEMINI_API_KEY", "test.key")
	t.Setenv("GEMINI_MODEL", "gemini-x?key=leak")
	if _, err := ConfiguredPlanner(); err == nil {
		t.Error("unsafe model path accepted")
	}
	t.Setenv("GEMINI_MODEL", "")
	if _, err := ConfiguredPlanner(); err != nil {
		t.Fatal(err)
	}
}
