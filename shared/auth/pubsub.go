package auth

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/api/idtoken"
)

type idTokenValidator func(context.Context, string, string) (*idtoken.Payload, error)

// NewPubSubPushAuthorizer validates the Google-signed OIDC token configured on
// a Pub/Sub push subscription and pins it to the dedicated push identity.
func NewPubSubPushAuthorizer(audience, serviceAccountEmail string) func(context.Context, string) error {
	audience = strings.TrimSpace(audience)
	serviceAccountEmail = strings.TrimSpace(serviceAccountEmail)
	return func(ctx context.Context, token string) error {
		return authorizePubSubPush(ctx, token, audience, serviceAccountEmail, idtoken.Validate)
	}
}

func authorizePubSubPush(ctx context.Context, token, audience, serviceAccountEmail string, validate idTokenValidator) error {
	if audience == "" || serviceAccountEmail == "" {
		return fmt.Errorf("Pub/Sub push audience and service account are required")
	}
	payload, err := validate(ctx, token, audience)
	if err != nil {
		return fmt.Errorf("validate Pub/Sub identity token: %w", err)
	}
	email, _ := payload.Claims["email"].(string)
	if !strings.EqualFold(strings.TrimSpace(email), serviceAccountEmail) {
		return fmt.Errorf("unexpected Pub/Sub push identity")
	}
	if verified, ok := payload.Claims["email_verified"].(bool); ok && !verified {
		return fmt.Errorf("Pub/Sub push identity email is not verified")
	}
	return nil
}
