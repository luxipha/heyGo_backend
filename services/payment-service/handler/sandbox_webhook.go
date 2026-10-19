package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/services/payment-service/service"
)

// Unsigned sandbox callbacks are hints to query Monnify, never payment evidence.
// Refunds and unsupported event types remain signature-only.
func (h *WebhookHandler) verifySandboxNotification(c *gin.Context, event *monnifyWebhook) bool {
	if !sandboxOutcomeAllowed(*event) || event.EventData.PaymentReference == "" || event.EventData.TransactionReference == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unsupported unsigned notification"})
		return false
	}
	if h.topups.Pool == nil || h.topups.Verifier == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sandbox verification unavailable"})
		return false
	}
	var amount int64
	var currency, transaction string
	// Resolve a locally initiated payment first; arbitrary references cannot trigger
	// provider requests. Top-ups and debt collections are always denominated in NGN.
	err := h.topups.Pool.QueryRow(c.Request.Context(), `
 SELECT amount,currency::TEXT,transaction_reference FROM payments WHERE payment_reference=$1
 UNION ALL
 SELECT amount_kobo,'NGN',COALESCE(provider_transaction_reference,'') FROM driver_operating_topups WHERE provider='monnify' AND provider_reference=$1
 UNION ALL
 SELECT amount_kobo,'NGN',COALESCE(provider_transaction_reference,'') FROM rider_no_show_debt_payments WHERE provider='monnify' AND provider_reference=$1`, event.EventData.PaymentReference).Scan(&amount, &currency, &transaction)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "payment not found"})
		return false
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "payment lookup unavailable"})
		return false
	}
	if transaction != "" && transaction != event.EventData.TransactionReference {
		c.JSON(http.StatusConflict, gin.H{"error": "transaction reference mismatch"})
		return false
	}
	verified, err := h.topups.Verifier.VerifyTransaction(c.Request.Context(), event.EventData.TransactionReference)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sandbox provider verification unavailable"})
		return false
	}
	if !matchesSandboxPayment(*event, verified, amount, currency) {
		c.JSON(http.StatusConflict, gin.H{"error": "sandbox payment verification mismatch"})
		return false
	}
	// Caller metadata is not trusted even after verifying the financial identity.
	event.EventData.Metadata = map[string]any{"verification": "monnify_sandbox_api"}
	return true
}

func matchesSandboxPayment(event monnifyWebhook, verified *service.VerifiedTransaction, amount int64, currency string) bool {
	if verified == nil || verified.PaymentReference != event.EventData.PaymentReference || verified.TransactionReference != event.EventData.TransactionReference || verified.CurrencyCode != currency || verified.PaymentStatus != event.EventData.PaymentStatus {
		return false
	}
	total, err := monnifyAmountKobo(verified.TotalPayable.String())
	if err != nil || total != amount {
		return false
	}
	if verified.PaymentStatus == "PAID" {
		paid, err := monnifyAmountKobo(verified.AmountPaid.String())
		if err != nil || paid != amount {
			return false
		}
	}
	return sandboxOutcomeAllowed(event)
}

func sandboxOutcomeAllowed(event monnifyWebhook) bool {
	switch event.EventType {
	case "SUCCESSFUL_TRANSACTION":
		return event.EventData.PaymentStatus == "PAID"
	case "REJECTED_PAYMENT":
		switch event.EventData.PaymentStatus {
		case "FAILED", "CANCELLED", "EXPIRED":
			return true
		}
	}
	return false
}
