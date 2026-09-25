package handler

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (h *WebhookHandler) createOperatingTopup(c *gin.Context) {
	if h.topups == nil || h.topups.InternalToken == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Internal-Service-Token")), []byte(h.topups.InternalToken)) != 1 {
		c.Status(http.StatusUnauthorized)
		return
	}
	var body struct {
		DriverID   string `json:"driverId"`
		Email      string `json:"email"`
		AmountKobo int64  `json:"amountKobo"`
	}
	if c.ShouldBindJSON(&body) != nil || body.AmountKobo <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_topup"})
		return
	}
	if _, err := uuid.Parse(body.DriverID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_driver"})
		return
	}
	address, err := mail.ParseAddress(body.Email)
	if err != nil || address.Address != body.Email || strings.ContainsAny(body.Email, "\r\n") {
		c.JSON(http.StatusBadRequest, gin.H{"code": "email_unavailable"})
		return
	}
	var market *string
	var recordedAt time.Time
	err = h.topups.Pool.QueryRow(c.Request.Context(), `SELECT driver_market_at(location),recorded_at
		FROM driver_live_locations WHERE driver_id=$1::UUID`, body.DriverID).Scan(&market, &recordedAt)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (market == nil || time.Since(recordedAt) > 30*time.Minute)) {
		c.JSON(http.StatusConflict, gin.H{"code": "topup_market_unavailable"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "topup_unavailable"})
		return
	}
	var count int
	var minimum, maximum *int64
	var policyID *string
	err = h.topups.Pool.QueryRow(c.Request.Context(), `SELECT COUNT(*),MIN(id::TEXT),MIN(topup_minimum_kobo),MIN(topup_maximum_kobo)
		FROM operating_balance_policies WHERE market_code=$1 AND status='approved'
		AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())`, *market).
		Scan(&count, &policyID, &minimum, &maximum)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "topup_unavailable"})
		return
	}
	if count != 1 || policyID == nil || minimum == nil || maximum == nil {
		c.JSON(http.StatusConflict, gin.H{"code": "topup_limits_unavailable"})
		return
	}
	if body.AmountKobo < *minimum || body.AmountKobo > *maximum {
		c.JSON(http.StatusBadRequest, gin.H{"code": "topup_amount_out_of_range", "minimumKobo": minimum, "maximumKobo": maximum})
		return
	}
	id := uuid.NewString()
	merchantReference := "heygo-ob-" + id
	_, err = h.topups.Pool.Exec(c.Request.Context(), `INSERT INTO driver_operating_topups(id,driver_id,amount_kobo,provider,provider_reference,status,market_code,policy_id)
		VALUES($1::UUID,$2::UUID,$3,'monnify',$4,'pending',$5,$6::UUID)`, id, body.DriverID, body.AmountKobo, merchantReference, *market, *policyID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "topup_unavailable"})
		return
	}
	session, err := h.topups.Initializer.CreateOperatingTopupSession(c.Request.Context(), body.AmountKobo, merchantReference, body.Email, body.DriverID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "checkout_unavailable"})
		return
	}
	tag, err := h.topups.Pool.Exec(c.Request.Context(), `UPDATE driver_operating_topups
		SET provider_transaction_reference=COALESCE(provider_transaction_reference,$2),checkout_url=$3
		WHERE id=$1::UUID AND (provider_transaction_reference IS NULL OR provider_transaction_reference=$2)`, id, session.TransactionReference, session.CheckoutURL)
	if err != nil || tag.RowsAffected() != 1 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "topup_unavailable"})
		return
	}
	c.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id, "amountKobo": body.AmountKobo,
		"marketCode": *market, "status": "pending", "checkoutUrl": session.CheckoutURL,
		"paymentReference": session.PaymentReference, "transactionReference": session.TransactionReference}})
}
