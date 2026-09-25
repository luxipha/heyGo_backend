package trust

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCasperIDReporterSubmitsBackendAuthenticatedEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/trust/events" || r.Header.Get("X-App-ID") != "app-1" || r.Header.Get("X-Api-Secret") != "secret" {
			t.Errorf("unexpected request metadata")
		}
		var event Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		if event.EventType != "interaction.completed" || event.UserID != "casper-user" {
			t.Errorf("unexpected event: %+v", event)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	reporter := NewCasperIDReporter(server.URL, "app-1", "secret")
	err := reporter.Submit(context.Background(), Event{UserID: "casper-user", ActorRole: "consumer", EventType: "interaction.completed", ExternalEventID: "event-1", InteractionReference: "trip-1", OccurredAt: time.Now()})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
}

func TestCasperIDReporterTreatsDuplicateAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusConflict) }))
	defer server.Close()
	if err := NewCasperIDReporter(server.URL, "app", "secret").Submit(context.Background(), Event{}); err != nil {
		t.Fatalf("duplicate must be idempotent: %v", err)
	}
}
