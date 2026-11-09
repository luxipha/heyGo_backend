package handler

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/services/payment-service/service"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/monnifysettings"
	"github.com/luxipha/heyGo_backend/shared/tripcollection"
	"net/mail"
	"sort"
	"strings"
	"time"
)

func (h *WebhookHandler) paymentAuthorized(c *gin.Context) bool {
	if h.topups == nil || h.topups.InternalToken == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Internal-Service-Token")), []byte(h.topups.InternalToken)) != 1 {
		c.JSON(401, gin.H{"code": "internal_auth_required"})
		return false
	}
	return true
}
func (h *WebhookHandler) testMonnify(c *gin.Context) {
	if !h.paymentAuthorized(c) {
		return
	}
	if h.topups.Configured == nil {
		c.JSON(503, gin.H{"code": "monnify_not_configured"})
		return
	}
	if e := h.topups.Configured.Ping(c.Request.Context()); e != nil {
		c.JSON(503, gin.H{"code": "monnify_authentication_failed"})
		return
	}
	s, e := monnifysettings.Read(c.Request.Context(), h.topups.Pool, 0)
	if e != nil {
		c.JSON(503, gin.H{"code": "monnify_settings_unavailable"})
		return
	}
	c.JSON(200, gin.H{"data": gin.H{"authenticated": true, "environment": s.Environment, "version": s.Version}})
}
func (h *WebhookHandler) createTripCheckout(c *gin.Context) {
	if !h.paymentAuthorized(c) {
		return
	}
	if h.topups.TripInitializer == nil {
		c.JSON(503, gin.H{"code": "trip_collection_unavailable"})
		return
	}
	var b struct {
		TripID  string `json:"tripId"`
		RiderID string `json:"riderId"`
	}
	if c.ShouldBindJSON(&b) != nil || uuid.Validate(b.TripID) != nil || uuid.Validate(b.RiderID) != nil {
		c.JSON(400, gin.H{"code": "invalid_trip_payment"})
		return
	}
	ctx := c.Request.Context()
	tx, e := h.topups.Pool.Begin(ctx)
	if e != nil {
		c.JSON(503, gin.H{"code": "payment_unavailable"})
		return
	}
	defer tx.Rollback(ctx)
	var email string
	e = tx.QueryRow(ctx, `SELECT email FROM users WHERE id=$1::uuid AND active AND email_verified AND 'rider'=ANY(roles) FOR SHARE`, b.RiderID).Scan(&email)
	if e != nil {
		c.JSON(409, gin.H{"code": "verified_email_required"})
		return
	}
	if _, e = mail.ParseAddress(email); e != nil {
		c.JSON(409, gin.H{"code": "verified_email_required"})
		return
	}
	// Same trip -> settlement lock ordering as completion and provider posting.
	var completed string
	if e = tx.QueryRow(ctx, `SELECT status FROM trips WHERE id=$1::uuid AND rider_id=$2::uuid FOR UPDATE`, b.TripID, b.RiderID).Scan(&completed); e != nil {
		c.JSON(404, gin.H{"code": "trip_not_found"})
		return
	}
	if completed != "completed" {
		c.JSON(409, gin.H{"code": "trip_not_completed"})
		return
	}
	var driver, status, method string
	var amount, debt int64
	e = tx.QueryRow(ctx, `SELECT driver_id::text,status,collection_method,expected_amount_kobo,no_show_debt_kobo FROM trip_settlements WHERE trip_id=$1::uuid AND rider_id=$2::uuid FOR UPDATE`, b.TripID, b.RiderID).Scan(&driver, &status, &method, &amount, &debt)
	if e != nil || status != "pending" || amount <= 0 {
		c.JSON(409, gin.H{"code": "trip_payment_not_due"})
		return
	}
	var ref string
	var checkout, savedTxn *string
	var token *string
	var lease *time.Time
	var savedStatus string
	e = tx.QueryRow(ctx, `SELECT payment_reference,checkout_url,transaction_reference,initialization_token::text,initialization_lease_until,status FROM rider_trip_payments WHERE trip_id=$1::uuid FOR UPDATE`, b.TripID).Scan(&ref, &checkout, &savedTxn, &token, &lease, &savedStatus)
	initialization := uuid.NewString()
	if errors.Is(e, pgx.ErrNoRows) {
		p, err := monnifysettings.Read(ctx, tx, 0)
		if err != nil || !p.Enabled || !p.TripPaymentsEnabled || !p.Configured() {
			c.JSON(409, gin.H{"code": "trip_collection_disabled"})
			return
		}
		ref = "heygo-ride-" + b.TripID
		_, e = tx.Exec(ctx, `INSERT INTO rider_trip_payments(trip_id,rider_id,driver_id,amount_kobo,fare_kobo,debt_kobo,payment_reference,monnify_config_version,initialization_token,initialization_lease_until)VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9::uuid,NOW()+INTERVAL '1 minute')`, b.TripID, b.RiderID, driver, amount, amount-debt, debt, ref, p.Version, initialization)
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE trip_settlements SET collection_method='monnify',updated_at=NOW() WHERE trip_id=$1::uuid`, b.TripID)
		}
		if e == nil {
			recipients := []string{b.RiderID, driver}
			sort.Strings(recipients)
			for _, recipient := range recipients {
				data, _ := json.Marshal(gin.H{"tripId": b.TripID, "status": "pending", "collectionMethod": "monnify", "providerVerified": false, "expectedAmountKobo": amount, "currency": "NGN"})
				if _, e = messaging.AppendEventTx(ctx, tx, recipient, "trip-collection-started:"+b.TripID, contracts.TripEventSettlementUpdated, data); e != nil {
					break
				}
			}
		}
	} else if e == nil {
		if savedStatus != "pending" {
			c.JSON(409, gin.H{"code": "payment_requires_review", "status": savedStatus})
			return
		}
		if checkout != nil && *checkout != "" {
			c.JSON(200, gin.H{"data": gin.H{"tripId": b.TripID, "paymentReference": ref, "checkoutUrl": *checkout, "amountKobo": amount, "currency": "NGN", "status": "pending"}})
			return
		}
		if lease != nil && lease.After(time.Now()) {
			c.JSON(409, gin.H{"code": "payment_initializing"})
			return
		}
		_, e = tx.Exec(ctx, `UPDATE rider_trip_payments SET initialization_token=$2::uuid,initialization_lease_until=NOW()+INTERVAL '1 minute' WHERE trip_id=$1::uuid`, b.TripID, initialization)
	}
	if e != nil {
		c.JSON(503, gin.H{"code": "payment_unavailable"})
		return
	}
	if e = tx.Commit(ctx); e != nil {
		c.JSON(503, gin.H{"code": "payment_unavailable"})
		return
	}
	session, e := h.topups.TripInitializer.CreatePaymentSession(ctx, ref, amount, "NGN", map[string]string{"tripID": b.TripID, "riderID": b.RiderID, "driverID": driver, "customerEmail": email})
	if e != nil {
		session, e = h.topups.TripInitializer.RecoverPaymentSession(ctx, ref, amount, "NGN")
	}
	if e != nil || session == nil || session.PaymentReference != ref || session.TransactionReference == "" || session.CheckoutURL == "" {
		c.JSON(503, gin.H{"code": "checkout_initialization_uncertain", "retryable": true})
		return
	}
	tag, e := h.topups.Pool.Exec(ctx, `UPDATE rider_trip_payments SET checkout_url=$3,transaction_reference=$4,initialization_token=NULL,initialization_lease_until=NULL,updated_at=NOW() WHERE trip_id=$1::uuid AND initialization_token=$2::uuid`, b.TripID, initialization, session.CheckoutURL, session.TransactionReference)
	if e != nil || tag.RowsAffected() != 1 {
		c.JSON(503, gin.H{"code": "checkout_persistence_uncertain"})
		return
	}
	c.JSON(200, gin.H{"data": gin.H{"tripId": b.TripID, "paymentReference": ref, "checkoutUrl": session.CheckoutURL, "amountKobo": amount, "currency": "NGN", "status": "pending"}})
}
func (h *WebhookHandler) refreshTripPayment(c *gin.Context) {
	if !h.paymentAuthorized(c) {
		return
	}
	var b struct {
		TripID  string `json:"tripId"`
		RiderID string `json:"riderId"`
	}
	if c.ShouldBindJSON(&b) != nil || uuid.Validate(b.TripID) != nil || uuid.Validate(b.RiderID) != nil {
		c.JSON(400, gin.H{"code": "invalid_trip_payment"})
		return
	}
	var ref string
	if h.topups.Pool.QueryRow(c.Request.Context(), `SELECT payment_reference FROM rider_trip_payments WHERE trip_id=$1::uuid AND rider_id=$2::uuid`, b.TripID, b.RiderID).Scan(&ref) != nil {
		c.JSON(404, gin.H{"code": "payment_not_found"})
		return
	}
	h.verifyAndPostTripPayment(c, ref)
}
func (h *WebhookHandler) verifyAndPostTripPayment(c *gin.Context, ref string) {
	result, problem := verifyPersistedTripPayment(c.Request.Context(), h.topups.Pool, h.topups.Verifier, ref)
	if problem != nil {
		c.JSON(problem.status, gin.H{"code": problem.code})
		return
	}
	c.JSON(200, gin.H{"data": result})
}

type tripVerifier interface {
	VerifyTransaction(context.Context, string) (*service.VerifiedTransaction, error)
}
type verificationProblem struct {
	status int
	code   string
}

func verifyPersistedTripPayment(ctx context.Context, pool *pgxpool.Pool, verifier tripVerifier, ref string) (gin.H, *verificationProblem) {
	var txn *string
	var expected int64
	var savedStatus string
	if verifier == nil {
		return nil, &verificationProblem{503, "payment_verification_unavailable"}
	}
	if e := pool.QueryRow(ctx, `SELECT transaction_reference,amount_kobo,status FROM rider_trip_payments WHERE payment_reference=$1`, ref).Scan(&txn, &expected, &savedStatus); e != nil {
		return nil, &verificationProblem{404, "payment_not_found"}
	}
	if savedStatus == "success" {
		return gin.H{"status": "success", "providerVerified": true}, nil
	}
	if txn == nil {
		return nil, &verificationProblem{409, "payment_initializing"}
	}
	v, e := verifier.VerifyTransaction(ctx, *txn)
	if e != nil || v == nil {
		return nil, &verificationProblem{503, "provider_verification_unavailable"}
	}
	total, te := monnifyAmountKobo(v.TotalPayable.String())
	paid, pe := monnifyAmountKobo(v.AmountPaid.String())
	if v.TransactionReference != *txn || v.PaymentReference != ref || v.CurrencyCode != "NGN" || te != nil || total != expected {
		return nil, &verificationProblem{409, "payment_verification_mismatch"}
	}
	state := strings.ToUpper(v.PaymentStatus)
	if state == "PAID" {
		if pe != nil || paid != expected {
			return nil, &verificationProblem{409, "payment_amount_mismatch"}
		}
		if e = tripcollection.Settle(ctx, pool, ref, *txn, expected); e != nil {
			return nil, &verificationProblem{503, "payment_posting_unavailable"}
		}
		return gin.H{"status": "success", "providerVerified": true}, nil
	}
	status := "pending"
	switch state {
	case "FAILED", "EXPIRED", "REJECTED":
		status = "failed"
	case "CANCELLED":
		status = "cancelled"
	}
	if status != "pending" {
		if _, e = pool.Exec(ctx, `UPDATE rider_trip_payments SET status=$2,verified_at=NOW(),updated_at=NOW() WHERE payment_reference=$1 AND status<>'success'`, ref, status); e != nil {
			return nil, &verificationProblem{503, "payment_unavailable"}
		}
	}
	return gin.H{"status": status, "providerVerified": false, "providerStatus": state}, nil
}
