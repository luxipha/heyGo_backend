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

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/env"
)

var noShowPaymentHTTPClient = &http.Client{Timeout: 20 * time.Second}

func registerRiderNoShowPaymentRoutes(rider *gin.RouterGroup, pool *pgxpool.Pool) {
	rider.GET("/no-show-debt-payments", func(c *gin.Context) {
		var at any
		var after any
		if raw := c.Query("cursor"); raw != "" {
			cursor, err := decodeDriverTripHistoryCursor(raw)
			if err != nil {
				driverError(c, 400, "invalid_cursor", "Payment cursor is invalid")
				return
			}
			at, after = cursor.OccurredAt, cursor.ID
		}
		user, _ := gatewayauth.CurrentUser(c)
		rows, err := pool.Query(c.Request.Context(), `SELECT id::TEXT,debt_id::TEXT,amount_kobo,status,checkout_url,created_at FROM rider_no_show_debt_payments WHERE rider_id=$1::UUID AND ($2::TIMESTAMPTZ IS NULL OR (created_at,id)<($2::TIMESTAMPTZ,$3::UUID)) ORDER BY created_at DESC,id DESC LIMIT 31`, user.ID, at, after)
		if err != nil {
			driverError(c, 503, "debt_payment_unavailable", "Debt payment history is unavailable")
			return
		}
		defer rows.Close()
		items := []gin.H{}
		cursors := []driverTripHistoryCursor{}
		for rows.Next() {
			var id, debt, state string
			var amount int64
			var link *string
			var created time.Time
			if err = rows.Scan(&id, &debt, &amount, &state, &link, &created); err != nil {
				driverError(c, 503, "debt_payment_unavailable", "Debt payment history is unavailable")
				return
			}
			items = append(items, gin.H{"id": id, "debtId": debt, "amountKobo": amount, "currency": "NGN", "status": state, "checkoutUrl": link, "createdAt": created})
			cursors = append(cursors, driverTripHistoryCursor{OccurredAt: created, ID: id})
		}
		if rows.Err() != nil {
			driverError(c, 503, "debt_payment_unavailable", "Debt payment history is unavailable")
			return
		}
		var next *string
		if len(items) > 30 {
			v := encodeDriverTripHistoryCursor(cursors[29])
			next = &v
			items = items[:30]
		}
		c.JSON(200, contracts.APIResponse{Data: gin.H{"items": items, "nextCursor": next}})
	})
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
		if !user.EmailVerified {
			driverError(ctx, http.StatusConflict, "email_not_verified", "Verify your account email before paying this debt")
			return
		}
		address, err := mail.ParseAddress(user.Email)
		if err != nil || address.Address != user.Email || strings.ContainsAny(user.Email, "\r\n") {
			driverError(ctx, http.StatusConflict, "email_required", "A valid account email is required to pay this debt")
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
		if err := sharedauth.AddCloudRunIdentity(req, env.GetString("PAYMENT_SERVICE_AUDIENCE", "")); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "debt_payment_unavailable", "Debt payment checkout is unavailable")
			return
		}
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
		case "checkout_unresolved":
			driverError(ctx, http.StatusServiceUnavailable, failure.Code, "Checkout creation is unresolved; recover payment status before retrying")
		case "email_not_verified":
			driverError(ctx, http.StatusConflict, failure.Code, "Verify your current account email before paying this debt")
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
