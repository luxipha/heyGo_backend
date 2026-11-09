package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
)

func TestSavedPlaceMetadataCacheIsolationExpiryAndFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req agent.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		place := agent.Place{ID: req.PlaceID, Provider: "google", Name: "Home", Address: "Verified address", Location: agent.Coordinate{Latitude: 6.4, Longitude: 3.4}}
		_ = json.NewEncoder(w).Encode(agent.Response{SchemaVersion: 1, ConfirmationRequired: true, State: "resolved", Place: &place, Candidates: []agent.Place{place}})
	}))
	defer server.Close()
	t.Setenv("RIDE_AGENT_URL", server.URL)
	t.Setenv("RIDE_AGENT_INTERNAL_TOKEN", strings.Repeat("x", 32))
	t.Setenv("RIDE_AGENT_AUDIENCE", "")
	a := &riderAccountAPI{}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if a.savedPlaceMetadata(ctx, "owner", "google", "place-a") == nil {
			t.Fatal("resolution failed")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("cache not reused")
	}
	if a.savedPlaceMetadata(ctx, "owner", "nominatim", "place-a") != nil {
		t.Fatal("cross-provider metadata returned")
	}
	if a.savedPlaceMetadata(ctx, "other", "google", "place-a") == nil || calls.Load() != 3 {
		t.Fatal("owner isolation failed")
	}
	a.placeMu.Lock()
	key := "owner\x00google\x00place-a"
	entry := a.placeCache[key]
	entry.ExpiresAt = time.Now().Add(-time.Second)
	a.placeCache[key] = entry
	a.placeMu.Unlock()
	t.Setenv("RIDE_AGENT_URL", "")
	if a.savedPlaceMetadata(ctx, "owner", "google", "place-a") != nil {
		t.Fatal("expired metadata fabricated on provider failure")
	}
}
