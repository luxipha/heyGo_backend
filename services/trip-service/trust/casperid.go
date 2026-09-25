package trust

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"net/http"
	"strings"
	"time"
)

type Event struct {
	UserID               string    `json:"user_id"`
	ActorRole            string    `json:"actor_role"`
	EventType            string    `json:"event_type"`
	ExternalEventID      string    `json:"external_event_id"`
	InteractionReference string    `json:"interaction_reference"`
	Rating               *int      `json:"rating,omitempty"`
	OccurredAt           time.Time `json:"occurred_at"`
}

type Reporter interface {
	Submit(context.Context, Event) error
}

type CasperIDReporter struct {
	endpoint, appID, secret string
	client                  *http.Client
}

func NewCasperIDReporter(baseURL, appID, secret string) *CasperIDReporter {
	return &CasperIDReporter{endpoint: strings.TrimSuffix(baseURL, "/") + "/api/trust/events", appID: appID, secret: secret, client: &http.Client{Timeout: 5 * time.Second}}
}

func (r *CasperIDReporter) Submit(ctx context.Context, event Event) error {
	ctx, span := otel.Tracer("casperid").Start(ctx, "casperid trust event")
	defer span.End()
	span.SetAttributes(attribute.String("trust.event_type", event.EventType), attribute.String("trust.actor_role", event.ActorRole))
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode CasperID trust event: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-ID", r.appID)
	req.Header.Set("X-Api-Secret", r.secret)
	res, err := r.client.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("submit CasperID trust event: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict {
		return nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		span.SetAttributes(attribute.Int("http.response.status_code", res.StatusCode))
		span.SetStatus(codes.Error, "CasperID error")
		return fmt.Errorf("submit CasperID trust event: status %d", res.StatusCode)
	}
	return nil
}
