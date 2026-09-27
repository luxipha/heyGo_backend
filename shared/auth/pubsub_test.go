package auth

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/api/idtoken"
)

func TestAuthorizePubSubPush(t *testing.T) {
	validate := func(_ context.Context, token, audience string) (*idtoken.Payload, error) {
		if token == "invalid" {
			return nil, errors.New("invalid signature")
		}
		if audience != "https://pubsub.heygo.internal/api-gateway" {
			t.Fatalf("audience=%q", audience)
		}
		return &idtoken.Payload{Claims: map[string]any{"email": token, "email_verified": true}}, nil
	}

	if err := authorizePubSubPush(context.Background(), "push@heygo-ng.iam.gserviceaccount.com", "https://pubsub.heygo.internal/api-gateway", "push@heygo-ng.iam.gserviceaccount.com", validate); err != nil {
		t.Fatal(err)
	}
	if err := authorizePubSubPush(context.Background(), "other@heygo-ng.iam.gserviceaccount.com", "https://pubsub.heygo.internal/api-gateway", "push@heygo-ng.iam.gserviceaccount.com", validate); err == nil {
		t.Fatal("expected identity mismatch")
	}
	if err := authorizePubSubPush(context.Background(), "invalid", "https://pubsub.heygo.internal/api-gateway", "push@heygo-ng.iam.gserviceaccount.com", validate); err == nil {
		t.Fatal("expected token validation failure")
	}
}

func TestAuthorizePubSubPushRequiresConfiguration(t *testing.T) {
	validate := func(context.Context, string, string) (*idtoken.Payload, error) {
		return &idtoken.Payload{}, nil
	}
	if err := authorizePubSubPush(context.Background(), "token", "", "push@example.com", validate); err == nil {
		t.Fatal("expected missing configuration error")
	}
}
