package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/observe/correlation"
)

const maxPushBodyBytes = 11 << 20

type pushOriginContextKey struct{}

func PushOrigin(ctx context.Context) (string, bool) {
	origin, ok := ctx.Value(pushOriginContextKey{}).(string)
	return origin, ok && origin != ""
}

// PushAuthorizer verifies the bearer token attached to a Pub/Sub push request.
// Private Cloud Run services may rely on Cloud Run IAM and pass nil. Public
// services must provide an application-level authorizer for their push paths.
type PushAuthorizer func(context.Context, string) error

// AuthorizeHandler applies the same bearer-token validation used by Pub/Sub
// push endpoints to other internal task endpoints on publicly reachable
// services.
func AuthorizeHandler(handler http.Handler, authorize PushAuthorizer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authorize == nil {
			handler.ServeHTTP(w, r)
			return
		}
		token, err := bearerToken(r.Header.Get("Authorization"))
		if err != nil || authorize(r.Context(), token) != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

type pushEnvelope struct {
	Message struct {
		Attributes  map[string]string `json:"attributes"`
		Data        []byte            `json:"data"`
		MessageID   string            `json:"messageId"`
		OrderingKey string            `json:"orderingKey"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

// NewPushHandler converts the wrapped Pub/Sub HTTP payload into the same
// transport-neutral Message used by streaming-pull consumers. A non-2xx
// response deliberately asks Pub/Sub to retry and eventually dead-letter the
// delivery.
func NewPushHandler(topic string, handler MessageHandler, authorize PushAuthorizer) http.Handler {
	handlerWithEnvelope := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if handler == nil {
			http.Error(w, "push handler unavailable", http.StatusServiceUnavailable)
			return
		}
		body := http.MaxBytesReader(w, r.Body, maxPushBodyBytes)
		defer body.Close()
		var delivery pushEnvelope
		decoder := json.NewDecoder(body)
		if err := decoder.Decode(&delivery); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				http.Error(w, "push payload too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid push envelope", http.StatusBadRequest)
			return
		}
		if err := ensureJSONEOF(decoder); err != nil {
			http.Error(w, "invalid push envelope", http.StatusBadRequest)
			return
		}
		if delivery.Message.MessageID == "" || len(delivery.Message.Data) == 0 {
			http.Error(w, "push message id and data are required", http.StatusBadRequest)
			return
		}

		var event contracts.EventMessage
		if err := json.Unmarshal(delivery.Message.Data, &event); err != nil {
			http.Error(w, "invalid event envelope", http.StatusBadRequest)
			return
		}
		if err := event.Validate(); err != nil {
			http.Error(w, "invalid event envelope", http.StatusBadRequest)
			return
		}
		if err := contracts.ValidateTopicPayload(topic, event.Data); err != nil {
			http.Error(w, "invalid event payload", http.StatusBadRequest)
			return
		}

		ctx := correlation.WithID(r.Context(), event.CorrelationID)
		ctx = context.WithValue(ctx, pushOriginContextKey{}, "https://"+r.Host)
		message := &Message{
			ID:         delivery.Message.MessageID,
			Topic:      topic,
			Key:        event.EntityID,
			Data:       delivery.Message.Data,
			Attributes: delivery.Message.Attributes,
		}
		if err := handler(ctx, message); err != nil {
			http.Error(w, "message processing failed", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return AuthorizeHandler(handlerWithEnvelope, authorize)
}

func NewPushMux(topics []string, handler MessageHandler, authorize PushAuthorizer) *http.ServeMux {
	mux := http.NewServeMux()
	for _, topic := range topics {
		topic := topic
		mux.Handle("POST /internal/pubsub/"+topic, NewPushHandler(topic, handler, authorize))
	}
	return mux
}

func bearerToken(header string) (string, error) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("bearer token is required")
	}
	return strings.TrimSpace(token), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("multiple JSON values")
}
