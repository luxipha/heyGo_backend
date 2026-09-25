package handler

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/luxipha/heyGo_backend/services/payment-service/repo"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (h *WebhookHandler) createNoShowDebtPayment(c *gin.Context) {
	if h.topups == nil || h.topups.InternalToken == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Internal-Service-Token")), []byte(h.topups.InternalToken)) != 1 {
		c.Status(http.StatusUnauthorized)
		return
	}
	var body struct {
		DebtID         string `json:"debtId"`
		RiderID        string `json:"riderId"`
		Email          string `json:"email"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if c.ShouldBindJSON(&body) != nil || body.IdempotencyKey == "" || len(body.IdempotencyKey) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_debt_payment"})
		return
	}
	if _, err := uuid.Parse(body.DebtID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_debt"})
		return
	}
	if _, err := uuid.Parse(body.RiderID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_rider"})
		return
	}
	address, err := mail.ParseAddress(body.Email)
	if err != nil || address.Address != body.Email || strings.ContainsAny(body.Email, "\r\n") {
		c.JSON(http.StatusBadRequest, gin.H{"code": "email_unavailable"})
		return
	}
	ctx := c.Request.Context()
	tx, err := h.topups.Pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "debt_payment_unavailable"})
		return
	}
	defer tx.Rollback(ctx)
	var existingID, existingDebt, existingStatus string
	var existingAmount int64
	var existingCheckout, existingReference, existingTransaction *string
	err = tx.QueryRow(ctx, `SELECT id::TEXT,debt_id::TEXT,amount_kobo,status,checkout_url,provider_reference,provider_transaction_reference
		FROM rider_no_show_debt_payments WHERE rider_id=$1::UUID AND idempotency_key=$2 FOR UPDATE`, body.RiderID, body.IdempotencyKey).
		Scan(&existingID, &existingDebt, &existingAmount, &existingStatus, &existingCheckout, &existingReference, &existingTransaction)
	if err == nil {
		if existingDebt != body.DebtID {
			c.JSON(http.StatusConflict, gin.H{"code": "idempotency_conflict"})
			return
		}
		if existingStatus != "pending" {
			c.JSON(http.StatusConflict, gin.H{"code": "debt_not_due"})
			return
		}
		if err := tx.Commit(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"code": "debt_payment_unavailable"})
			return
		}
		c.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": existingID, "debtId": existingDebt, "amountKobo": existingAmount, "currency": "NGN", "status": existingStatus, "checkoutUrl": existingCheckout, "paymentReference": existingReference, "transactionReference": existingTransaction}})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "debt_payment_unavailable"})
		return
	}
	var amount int64
	err = tx.QueryRow(ctx, `SELECT amount_kobo FROM rider_no_show_debts WHERE id=$1::UUID AND rider_id=$2::UUID AND status='due' FOR UPDATE`, body.DebtID, body.RiderID).Scan(&amount)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusConflict, gin.H{"code": "debt_not_due"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "debt_payment_unavailable"})
		return
	}
	// Repeat the idempotency lookup after locking the debt. Concurrent requests
	// may have passed the first lookup before either one acquired that lock.
	err = tx.QueryRow(ctx, `SELECT id::TEXT,debt_id::TEXT,amount_kobo,status,checkout_url,provider_reference,provider_transaction_reference
		FROM rider_no_show_debt_payments WHERE rider_id=$1::UUID AND idempotency_key=$2 FOR UPDATE`, body.RiderID, body.IdempotencyKey).
		Scan(&existingID, &existingDebt, &existingAmount, &existingStatus, &existingCheckout, &existingReference, &existingTransaction)
	if err == nil {
		if existingDebt != body.DebtID {
			c.JSON(http.StatusConflict, gin.H{"code": "idempotency_conflict"})
			return
		}
		if existingStatus != "pending" {
			c.JSON(http.StatusConflict, gin.H{"code": "debt_not_due"})
			return
		}
		if err := tx.Commit(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"code": "debt_payment_unavailable"})
			return
		}
		c.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": existingID, "debtId": existingDebt, "amountKobo": existingAmount, "currency": "NGN", "status": existingStatus, "checkoutUrl": existingCheckout, "paymentReference": existingReference, "transactionReference": existingTransaction}})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "debt_payment_unavailable"})
		return
	}
	var pendingID string
	err = tx.QueryRow(ctx, `SELECT id::TEXT FROM rider_no_show_debt_payments WHERE debt_id=$1::UUID AND status='pending' FOR UPDATE`, body.DebtID).Scan(&pendingID)
	if err == nil {
		c.JSON(http.StatusConflict, gin.H{"code": "debt_payment_pending"})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "debt_payment_unavailable"})
		return
	}
	id := uuid.NewString()
	merchantReference := "heygo-nsd-" + id
	if _, err := tx.Exec(ctx, `INSERT INTO rider_no_show_debt_payments(id,debt_id,rider_id,amount_kobo,provider,provider_reference,idempotency_key,status)
		VALUES($1::UUID,$2::UUID,$3::UUID,$4,'monnify',$5,$6,'pending')`, id, body.DebtID, body.RiderID, amount, merchantReference, body.IdempotencyKey); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "debt_payment_unavailable"})
		return
	}
	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "debt_payment_unavailable"})
		return
	}
	session, err := h.topups.Initializer.CreateNoShowDebtPaymentSession(ctx, amount, merchantReference, body.Email, body.RiderID, body.DebtID)
	if err != nil {
		_, _ = h.topups.Pool.Exec(ctx, `UPDATE rider_no_show_debt_payments SET status='failed' WHERE id=$1::UUID AND status='pending'`, id)
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "checkout_unavailable"})
		return
	}
	tag, err := h.topups.Pool.Exec(ctx, `UPDATE rider_no_show_debt_payments SET provider_transaction_reference=$2,checkout_url=$3 WHERE id=$1::UUID AND status='pending'`, id, session.TransactionReference, session.CheckoutURL)
	if err != nil || tag.RowsAffected() != 1 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "debt_payment_unavailable"})
		return
	}
	c.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id, "debtId": body.DebtID, "amountKobo": amount, "currency": "NGN", "status": "pending", "checkoutUrl": session.CheckoutURL, "paymentReference": session.PaymentReference, "transactionReference": session.TransactionReference}})
}

func (h *WebhookHandler) handleNoShowDebtPayment(c *gin.Context, event monnifyWebhook) {
	if strings.ToUpper(event.EventType) != "SUCCESSFUL_TRANSACTION" {
		c.Status(http.StatusOK)
		return
	}
	verified, err := h.topups.Verifier.VerifyTransaction(c.Request.Context(), event.EventData.TransactionReference)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "debt payment verification unavailable"})
		return
	}
	if verified.PaymentStatus != "PAID" || verified.CurrencyCode != "NGN" || verified.PaymentReference != event.EventData.PaymentReference || verified.TransactionReference != event.EventData.TransactionReference {
		c.JSON(http.StatusConflict, gin.H{"error": "debt payment verification mismatch"})
		return
	}
	amountKobo, err := monnifyAmountKobo(verified.AmountPaid.String())
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "invalid verified amount"})
		return
	}
	err = repo.PostVerifiedNoShowDebtPayment(c.Request.Context(), h.topups.Pool, verified.PaymentReference, verified.TransactionReference, amountKobo)
	if errors.Is(err, repo.ErrNoShowDebtPaymentMismatch) || errors.Is(err, repo.ErrNoShowDebtPaymentNotFound) {
		c.JSON(http.StatusConflict, gin.H{"error": "debt payment verification mismatch"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "debt payment posting unavailable"})
		return
	}
	c.Status(http.StatusOK)
}
