package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGooglePlaceDetailsDoesNotSendGETPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method != "GET" || len(raw) != 0 {
			t.Errorf("details must use a bodyless GET, got %s %q", r.Method, raw)
		}
		if r.Header.Get("X-Goog-FieldMask") != "id,displayName,formattedAddress,location" {
			t.Error("unexpected billed fields")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"test-id","displayName":{"text":"Test place"},"location":{"latitude":6.5,"longitude":3.4}}`))
	}))
	defer server.Close()
	p, err := (GooglePlaces{Key: "synthetic-test-key", BaseURL: server.URL, Client: server.Client()}).Resolve(context.Background(), "test-id")
	if err != nil || p.ID != "test-id" {
		t.Fatalf("resolve failed %v", err)
	}
}
