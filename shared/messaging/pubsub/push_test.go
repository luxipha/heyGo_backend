package pubsub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func pushBody(t *testing.T, event contracts.EventMessage) string {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"messageId": "delivery-1",
			"data":      base64.StdEncoding.EncodeToString(raw),
			"attributes": map[string]string{
				"event_type": contracts.DriverCmdLocation,
			},
		},
		"subscription": "projects/heygo-ng/subscriptions/driver-location",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func validLocationEvent() contracts.EventMessage {
	return contracts.EventMessage{
		Version:  contracts.EventSchemaVersion,
		EventID:  "event-1",
		EntityID: "driver-1",
		Data:     []byte(`{"location":{"latitude":6.5,"longitude":3.4}}`),
	}
}

func TestPushHandlerDeliversValidatedMessage(t *testing.T) {
	var got *Message
	h := NewPushHandler(contracts.DriverCmdLocation, func(_ context.Context, message *Message) error {
		got = message
		return nil
	}, nil)
	req := httptest.NewRequest(http.MethodPost, "/internal/pubsub/driver.cmd.location", strings.NewReader(pushBody(t, validLocationEvent())))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if got == nil || got.ID != "delivery-1" || got.Topic != contracts.DriverCmdLocation || got.Key != "driver-1" {
		t.Fatalf("unexpected message: %#v", got)
	}
}

func TestPushHandlerRequiresValidAuthorization(t *testing.T) {
	authorize := func(_ context.Context, token string) error {
		if token != "expected" {
			return errors.New("wrong token")
		}
		return nil
	}
	h := NewPushHandler(contracts.DriverCmdLocation, func(context.Context, *Message) error { return nil }, authorize)

	for _, tc := range []struct {
		name   string
		header string
		want   int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "wrong", header: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "valid", header: "Bearer expected", want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(pushBody(t, validLocationEvent())))
			req.Header.Set("Authorization", tc.header)
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("status=%d want=%d", res.Code, tc.want)
			}
		})
	}
}

func TestPushHandlerRejectsMalformedMessages(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid json", body: `{`},
		{name: "missing message", body: `{}`},
		{name: "invalid event", body: `{"message":{"messageId":"delivery-1","data":"bm90LWpzb24="}}`},
		{name: "invalid payload", body: pushBody(t, contracts.EventMessage{Version: "1", EventID: "event-1", EntityID: "driver-1", Data: []byte(`{}`)})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewPushHandler(contracts.DriverCmdLocation, func(context.Context, *Message) error { return nil }, nil)
			res := httptest.NewRecorder()
			h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body)))
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestPushHandlerRetriesProcessingFailure(t *testing.T) {
	h := NewPushHandler(contracts.DriverCmdLocation, func(context.Context, *Message) error {
		return errors.New("temporary")
	}, nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(pushBody(t, validLocationEvent()))))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}
