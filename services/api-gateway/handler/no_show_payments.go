package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"

	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var noShowPaymentHTTPClient = &http.Client{Timeout: 20 * time.Second}

func registerRiderNoShowPaymentRoutes(rider *gin.RouterGroup, pool *pgxpool.Pool) {
	rider.POST("/no-show-debts/:id/pay", func(ctx *gin.Context) {
		debtID := ctx.Param("id")
		if _, err := uuid.Parse(debtID); err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_debt_id", "Valid debt ID is required")
			return
		}
		key := strings.TrimSpace(ctx.GetHeader("Idempotency-Key"))
		if key == "" || len(key) > 200 {
			driverError(ctx, http.StatusBadRequest, "idempotency_key_required", "A valid Idempotency-Key is required")
			return
		}
		user, _ := gatewayauth.CurrentUser(ctx)
		address, err := mail.ParseAddress(user.Email)
		if err != nil || address.Address != user.Email || strings.ContainsAny(user.Email, "\r\n") {
			driverError(ctx, http.StatusConflict, "casperid_email_required", "A verified CasperID email is required to pay this debt")
			return
		}
		payload, _ := json.Marshal(gin.H{"debtId": debtID, "riderId": user.ID, "email": user.Email, "idempotencyKey": key})
		req, err := http.NewRequestWithContext(ctx.Request.Context(), http.MethodPost,
			strings.TrimRight(env.GetString("PAYMENT_SERVICE_URL", "http://payment-service:9200"), "/")+"/internal/no-show-debt-payments", bytes.NewReader(payload))
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "debt_payment_unavailable", "Debt payment checkout is unavailable")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Service-Token", env.GetString("INTERNAL_SERVICE_TOKEN", ""))
		res, err := noShowPaymentHTTPClient.Do(req)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "debt_payment_unavailable", "Debt payment checkout is unavailable")
			return
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "debt_payment_unavailable", "Debt payment checkout is unavailable")
			return
		}
		if res.StatusCode == http.StatusCreated && json.Valid(body) {
			ctx.Data(http.StatusCreated, "application/json", body)
			return
		}
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(body, &failure)
		switch failure.Code {
		case "debt_not_due":
			driverError(ctx, http.StatusConflict, failure.Code, "This no-show debt is no longer due")
		case "idempotency_conflict":
			driverError(ctx, http.StatusConflict, failure.Code, "This Idempotency-Key was already used for another debt payment")
		case "debt_payment_pending":
			driverError(ctx, http.StatusConflict, failure.Code, "A payment checkout is already open for this debt")
		default:
			driverError(ctx, http.StatusServiceUnavailable, "debt_payment_unavailable", "Debt payment checkout is unavailable")
		}
	})

	rider.GET("/no-show-debt-payments/:id", func(ctx *gin.Context) {
		id := ctx.Param("id")
		if _, err := uuid.Parse(id); err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_payment_id", "Valid payment ID is required")
			return
		}
		user, _ := gatewayauth.CurrentUser(ctx)
		var debtID, status string
		var amount int64
		var checkoutURL, transactionReference *string
		var created time.Time
		err := pool.QueryRow(ctx.Request.Context(), `SELECT debt_id::TEXT,amount_kobo,status,checkout_url,provider_transaction_reference,created_at
			FROM rider_no_show_debt_payments WHERE id=$1::UUID AND rider_id=$2::UUID`, id, user.ID).
			Scan(&debtID, &amount, &status, &checkoutURL, &transactionReference, &created)
		if errors.Is(err, pgx.ErrNoRows) {
			driverError(ctx, http.StatusNotFound, "debt_payment_not_found", "Debt payment was not found")
			return
		}
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "debt_payment_unavailable", "Debt payment status is unavailable")
			return
		}
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "debtId": debtID, "amountKobo": amount, "currency": "NGN", "status": status, "checkoutUrl": checkoutURL, "transactionReference": transactionReference, "createdAt": created}})
	})
}
