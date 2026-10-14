package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePlaces struct {
	calls  int
	query  string
	places []Place
}

func (f *fakePlaces) Search(_ context.Context, q string, _ *Coordinate) ([]Place, error) {
	f.calls++
	f.query = q
	return f.places, nil
}
func (f *fakePlaces) Resolve(_ context.Context, id string) (Place, error) {
	f.calls++
	return f.places[0], nil
}

type fakePlanner struct {
	calls int
	plan  Plan
	err   error
}

func (p *fakePlanner) Plan(context.Context, string) (Plan, error) { p.calls++; return p.plan, p.err }
func fixture() Place {
	return Place{ID: "stable_place", Name: "Lekki", Address: "Lagos", Location: Coordinate{6.45, 3.45}, Provider: "google"}
}
func TestSensitiveIntentsNeverReachTools(t *testing.T) {
	for _, text := range []string{"cancel my trip", "wallet balance", "delete account", "admin drivers", "email a@example.com", "OTP 12345678", "book a ride"} {
		p := &fakePlaces{places: []Place{fixture()}}
		llm := &fakePlanner{}
		reply, err := (&Gateway{Places: p, Planner: llm, LLMEnabled: true}).Resolve(context.Background(), Request{SchemaVersion: 1, Message: text})
		if err != nil || reply.State != "unsupported" || p.calls != 0 || llm.calls != 0 {
			t.Fatalf("%s: %#v err=%v tools=%d/%d", text, reply, err, p.calls, llm.calls)
		}
	}
}
func TestDeterministicFallbackAndClarification(t *testing.T) {
	for _, planner := range []*fakePlanner{{plan: Plan{Tool: "dispatch", Query: "Lekki"}}, {err: errors.New("offline")}, {plan: Plan{Tool: "search_places", Query: "wallet"}}} {
		p := &fakePlaces{places: []Place{fixture()}}
		g := Gateway{Places: p, Planner: planner, LLMEnabled: true}
		got, err := g.Resolve(context.Background(), Request{SchemaVersion: 1, Message: "take me to Lekki"})
		if err != nil || got.Planner != "deterministic" || p.query != "Lekki" || got.State != "resolved" || !got.ConfirmationRequired {
			t.Fatalf("got=%#v err=%v", got, err)
		}
	}
	p := &fakePlaces{places: []Place{fixture(), fixture(), {ID: "second", Name: "Lekki 2", Location: Coordinate{6.5, 3.4}}}}
	got, err := (&Gateway{Places: p}).Resolve(context.Background(), Request{SchemaVersion: 1, Message: "Lekki"})
	if err != nil || got.State != "clarification" || len(got.Candidates) != 2 || got.Place != nil {
		t.Fatalf("clarification=%#v err=%v", got, err)
	}
	got, err = (&Gateway{Places: p}).Resolve(context.Background(), Request{SchemaVersion: 1, PlaceID: "wrong"})
	if err == nil {
		t.Fatalf("provider must resolve requested identity: %#v", got)
	}
}
func TestSchemaAndInternalAuthentication(t *testing.T) {
	g := Gateway{InternalToken: strings.Repeat("x", 32), Places: &fakePlaces{places: []Place{fixture()}}}
	for _, tc := range []struct {
		body, auth string
		status     int
	}{{`{"schemaVersion":1,"message":"Lekki"}`, "", 401}, {`{"schemaVersion":1,"message":"Lekki","dispatch":true}`, "Bearer " + g.InternalToken, 400}, {`{"schemaVersion":1,"message":"Lekki"} {}`, "Bearer " + g.InternalToken, 400}, {`{"schemaVersion":1,"message":"Lekki"}`, "Bearer " + g.InternalToken, 200}} {
		req := httptest.NewRequest(http.MethodPost, "/v1/resolve", strings.NewReader(tc.body))
		req.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestGoogleAdapterUsesServerKeyAndNigerianSearchContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") != "test-server-only" || r.Header.Get("X-Goog-FieldMask") == "" {
			t.Error("missing private provider headers")
		}
		if r.URL.Path != "/places:searchText" || r.Method != "POST" {
			t.Error("unexpected provider operation")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["regionCode"] != "NG" || body["textQuery"] != "Lekki" || body["userId"] != nil || body["accessToken"] != nil {
			t.Errorf("unsafe search body: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"places":[{"id":"stable_place","displayName":{"text":"Lekki"},"formattedAddress":"Lagos","location":{"latitude":6.45,"longitude":3.45}}]}`))
	}))
	defer server.Close()
	provider := GooglePlaces{Key: "test-server-only", BaseURL: server.URL, Client: server.Client()}
	places, err := provider.Search(context.Background(), "Lekki", &Coordinate{6.45, 3.45})
	if err != nil || len(places) != 1 || places[0].ID != "stable_place" || places[0].Provider != "google" {
		t.Fatalf("places=%v err=%v", places, err)
	}
}
