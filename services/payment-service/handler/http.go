package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cprakhar/uber-clone/services/payment-service/repo"
	"github.com/cprakhar/uber-clone/services/payment-service/service"
	"github.com/cprakhar/uber-clone/services/payment-service/types"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/httpmiddleware"
	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/cprakhar/uber-clone/shared/messaging/kafka"
	"github.com/cprakhar/uber-clone/shared/observe/metrics"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

type WebhookHandler struct {
	secret string
	svc    repo.Service
	kafka  *kafka.KafkaClient
	topups *OperatingTopupHandler
}

type OperatingTopupHandler struct {
	Pool          *pgxpool.Pool
	InternalToken string
	Initializer   interface {
		CreateOperatingTopupSession(context.Context, int64, string, string, string) (*types.ProviderSession, error)
		CreateNoShowDebtPaymentSession(context.Context, int64, string, string, string, string) (*types.ProviderSession, error)
	}
	Verifier interface {
		VerifyTransaction(context.Context, string) (*service.VerifiedTransaction, error)
	}
}

type monnifyWebhook struct {
	EventType string `json:"eventType"`
	EventData struct {
		PaymentReference     string         `json:"paymentReference"`
		TransactionReference string         `json:"transactionReference"`
		PaymentStatus        string         `json:"paymentStatus"`
		Metadata             map[string]any `json:"metaData"`
	} `json:"eventData"`
}

func NewHTTPHandler(secret string, svc repo.Service, kf *kafka.KafkaClient, readiness ...func(context.Context) error) *gin.Engine {
	return NewHTTPHandlerWithTopups(secret, svc, kf, nil, readiness...)
}

func NewHTTPHandlerWithTopups(secret string, svc repo.Service, kf *kafka.KafkaClient, topups *OperatingTopupHandler, readiness ...func(context.Context) error) *gin.Engine {
	r := gin.Default()
	r.Use(otelgin.Middleware("payment-service"))
	r.Use(httpmiddleware.NewRateLimiter(240, time.Minute).Middleware)
	r.Use(metrics.HTTPMiddleware("payment-service"))
	h := &WebhookHandler{secret: secret, svc: svc, kafka: kf, topups: topups}
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "payment-service"}) })
	r.GET("/metrics", gin.WrapH(metrics.Handler()))
	r.GET("/ready", func(c *gin.Context) {
		if len(readiness) > 0 {
			if err := readiness[0](c.Request.Context()); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "error": err.Error()})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready", "service": "payment-service"})
	})
	r.POST("/webhooks/moniepoint", h.HandleMoniepoint)
	if topups != nil {
		r.POST("/internal/operating-topups", h.createOperatingTopup)
		r.POST("/internal/no-show-debt-payments", h.createNoShowDebtPayment)
	}
	return r
}

func (h *WebhookHandler) HandleMoniepoint(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if !ValidMonnifySignature(body, c.GetHeader("monnify-signature"), h.secret) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid webhook signature"})
		return
	}

	var event monnifyWebhook
	if err := json.Unmarshal(body, &event); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid webhook payload"})
		return
	}
	if event.EventData.PaymentReference == "" || event.EventData.TransactionReference == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payment and transaction references are required"})
		return
	}
	if h.topups != nil {
		var exists bool
		err := h.topups.Pool.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM rider_no_show_debt_payments WHERE provider='monnify' AND provider_reference=$1)`, event.EventData.PaymentReference).Scan(&exists)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "debt payment lookup unavailable"})
			return
		}
		if exists {
			h.handleNoShowDebtPayment(c, event)
			return
		}
		err = h.topups.Pool.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_operating_topups WHERE provider='monnify' AND provider_reference=$1)`, event.EventData.PaymentReference).Scan(&exists)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "top-up lookup unavailable"})
			return
		}
		if exists {
			h.handleOperatingTopup(c, event)
			return
		}
	}
	status, topic, ok := webhookOutcome(event.EventType, event.EventData.PaymentStatus)
	if !ok {
		c.Status(http.StatusNoContent)
		return
	}

	metadata := event.EventData.Metadata
	if metadata == nil {
		metadata = make(map[string]any)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err == nil {
		metadata["webhook"] = raw
	}
	eventID := strings.Join([]string{event.EventType, event.EventData.TransactionReference, string(status)}, ":")
	payment, publish, err := h.svc.ApplyWebhook(c, repo.WebhookUpdate{
		EventID: eventID, PaymentReference: event.EventData.PaymentReference,
		TransactionReference: event.EventData.TransactionReference, Status: status,
		ProviderMetadata: metadata,
	})
	if errors.Is(err, repo.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "payment not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not persist payment update"})
		return
	}
	if !publish {
		c.Status(http.StatusOK)
		return
	}

	payload, err := json.Marshal(messaging.PaymentStatusUpdateData{
		EventID: eventID, TripID: payment.TripID, RiderID: payment.RiderID, DriverID: payment.DriverID,
		PaymentReference: payment.PaymentReference, TransactionReference: payment.TransactionReference,
		Status: string(status),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not encode payment event"})
		return
	}
	if err := h.kafka.Producer.SendMessageAndWait(c, topic, &contracts.KafkaMessage{EntityID: payment.RiderID, Data: payload}, 10*time.Second); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "could not publish payment event"})
		return
	}
	if err := h.svc.MarkEventPublished(c, eventID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not finalize webhook event"})
		return
	}
	c.Status(http.StatusOK)
}

func (h *WebhookHandler) handleOperatingTopup(c *gin.Context, event monnifyWebhook) {
	if strings.ToUpper(event.EventType) != "SUCCESSFUL_TRANSACTION" {
		c.Status(http.StatusOK)
		return
	}
	verified, err := h.topups.Verifier.VerifyTransaction(c.Request.Context(), event.EventData.TransactionReference)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "top-up verification unavailable"})
		return
	}
	if verified.PaymentStatus != "PAID" || verified.CurrencyCode != "NGN" || verified.PaymentReference != event.EventData.PaymentReference {
		c.JSON(http.StatusConflict, gin.H{"error": "top-up verification mismatch"})
		return
	}
	amountKobo, err := monnifyAmountKobo(verified.AmountPaid.String())
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "invalid verified amount"})
		return
	}
	_, err = repo.PostVerifiedOperatingTopup(c.Request.Context(), h.topups.Pool, verified.PaymentReference, verified.TransactionReference, amountKobo)
	if errors.Is(err, repo.ErrTopupMismatch) || errors.Is(err, repo.ErrTopupNotFound) {
		c.JSON(http.StatusConflict, gin.H{"error": "top-up verification mismatch"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "top-up posting unavailable"})
		return
	}
	c.Status(http.StatusOK)
}

func monnifyAmountKobo(raw string) (int64, error) {
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || len(parts) == 0 || parts[0] == "" {
		return 0, fmt.Errorf("invalid monetary amount")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 || whole > (9223372036854775807-99)/100 {
		return 0, fmt.Errorf("invalid monetary amount")
	}
	fraction := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) == 0 || len(parts[1]) > 2 {
			return 0, fmt.Errorf("invalid monetary amount")
		}
		padded := parts[1] + strings.Repeat("0", 2-len(parts[1]))
		fraction, err = strconv.ParseInt(padded, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid monetary amount")
		}
	}
	amount := whole*100 + fraction
	if amount <= 0 {
		return 0, fmt.Errorf("invalid monetary amount")
	}
	return amount, nil
}

func ValidMonnifySignature(body []byte, signature, secret string) bool {
	if signature == "" || secret == "" {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return false
	}
	mac := hmac.New(sha512.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

func webhookOutcome(eventType, paymentStatus string) (types.PaymentStatus, string, bool) {
	status := strings.ToUpper(paymentStatus)
	switch strings.ToUpper(eventType) {
	case "SUCCESSFUL_TRANSACTION":
		return types.PaymentStatusSuccess, contracts.PaymentEventSuccess, true
	case "REJECTED_PAYMENT":
		if status == "CANCELLED" || status == "EXPIRED" {
			return types.PaymentStatusCancelled, contracts.PaymentEventCancelled, true
		}
		return types.PaymentStatusFailed, contracts.PaymentEventFailed, true
	default:
		return "", "", false
	}
}

func ListenAndServe(ctxDone <-chan struct{}, addr string, router http.Handler) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		<-ctxDone
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("payment HTTP server: %w", err)
	}
	return nil
}
