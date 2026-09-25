package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"

	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var topupHTTPClient = &http.Client{Timeout: 20 * time.Second}

func (a *driverAPI) createOperatingTopup(ctx *gin.Context) {
	var body struct {
		AmountKobo int64 `json:"amountKobo"`
	}
	if ctx.ShouldBindJSON(&body) != nil || body.AmountKobo <= 0 {
		driverError(ctx, http.StatusBadRequest, "invalid_topup", "A positive top-up amount in kobo is required")
		return
	}
	if strings.TrimSpace(ctx.GetHeader("Idempotency-Key")) == "" {
		driverError(ctx, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required for top-ups")
		return
	}
	user, _ := gatewayauth.CurrentUser(ctx)
	address, err := mail.ParseAddress(user.Email)
	if err != nil || address.Address != user.Email || strings.ContainsAny(user.Email, "\r\n") {
		driverError(ctx, http.StatusConflict, "casperid_email_required", "A CasperID email is required to top up")
		return
	}
	payload, _ := json.Marshal(gin.H{"driverId": user.ID, "email": user.Email, "amountKobo": body.AmountKobo})
	baseURL := env.GetString("PAYMENT_SERVICE_URL", "http://payment-service:9200")
	req, err := http.NewRequestWithContext(ctx.Request.Context(), http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/internal/operating-topups", bytes.NewReader(payload))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "topup_unavailable", "Top-up checkout is unavailable")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Token", env.GetString("INTERNAL_SERVICE_TOKEN", ""))
	res, err := topupHTTPClient.Do(req)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "topup_unavailable", "Top-up checkout is unavailable")
		return
	}
	defer res.Body.Close()
	response, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "topup_unavailable", "Top-up checkout is unavailable")
		return
	}
	if res.StatusCode == http.StatusCreated && json.Valid(response) {
		ctx.Data(http.StatusCreated, "application/json", response)
		return
	}
	var failure struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(response, &failure)
	switch failure.Code {
	case "topup_market_unavailable":
		driverError(ctx, http.StatusConflict, failure.Code, "Share a location from the last 30 minutes in an active HeyGo market")
	case "topup_limits_unavailable":
		driverError(ctx, http.StatusConflict, failure.Code, "Top-ups are unavailable in this area right now")
	case "topup_amount_out_of_range":
		driverError(ctx, http.StatusBadRequest, failure.Code, "Amount is outside this market's approved top-up limits")
	default:
		driverError(ctx, http.StatusServiceUnavailable, "topup_unavailable", "Top-up checkout is unavailable")
	}
}

func (a *driverAPI) operatingTopup(ctx *gin.Context) {
	id := ctx.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_topup_id", "Valid top-up ID is required")
		return
	}
	var amount int64
	var market, checkoutURL, paymentReference *string
	var status string
	var createdAt time.Time
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT amount_kobo,market_code,checkout_url,provider_reference,status,created_at
		FROM driver_operating_topups WHERE id=$1::UUID AND driver_id=$2::UUID`, id, driverID(ctx)).
		Scan(&amount, &market, &checkoutURL, &paymentReference, &status, &createdAt)
	if err == pgx.ErrNoRows {
		driverError(ctx, http.StatusNotFound, "topup_not_found", "Top-up was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "topup_unavailable", "Top-up is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": gin.H{"id": id, "amountKobo": amount, "marketCode": market,
		"checkoutUrl": checkoutURL, "paymentReference": paymentReference, "status": status, "createdAt": createdAt}})
}
