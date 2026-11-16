package handler

import (
	"crypto/subtle"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/services/payment-service/service"

	"net/http"
)

func (h *WebhookHandler) heroTransfer(c *gin.Context) {
	if h.topups == nil || h.topups.InternalToken == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Internal-Service-Token")), []byte(h.topups.InternalToken)) != 1 {
		c.JSON(401, gin.H{"code": "internal_auth_required"})
		return
	}
	if h.topups.Configured == nil {
		c.JSON(503, gin.H{"code": "monnify_unavailable"})
		return
	}
	var b struct {
		ID      string `json:"id"`
		Action  string `json:"action"`
		Account string `json:"accountNumber"`
		Code    string `json:"authorizationCode"`
	}
	if c.ShouldBindJSON(&b) != nil {
		c.JSON(400, gin.H{"code": "invalid_request"})
		return
	}
	if b.Action == "ready" {
		config, err := h.topups.Configured.Config(c.Request.Context(), "")
		if err != nil || len(config.DisbursementSource) != 10 {
			c.JSON(503, gin.H{"code": "disbursement_configuration_required"})
			return
		}
		c.JSON(200, gin.H{"data": gin.H{"ready": true}})
		return
	}
	if b.Action == "banks" {
		config, err := h.topups.Configured.Config(c.Request.Context(), "")
		if err != nil {
			c.JSON(503, gin.H{"code": "monnify_unavailable"})
			return
		}
		banks, err := service.NewMonnifyClient(config).Banks(c.Request.Context())
		if err != nil {
			c.JSON(503, gin.H{"code": "banks_unavailable"})
			return
		}
		c.JSON(200, gin.H{"data": banks})
		return
	}
	if _, err := uuid.Parse(b.ID); err != nil {
		c.JSON(400, gin.H{"code": "invalid_payout"})
		return
	}
	var ref, bank, status string
	var amount int64
	err := h.topups.Pool.QueryRow(c.Request.Context(), `SELECT reference,amount_kobo,b.bank_code,p.status FROM hero_stage_payouts p JOIN heroes_bank_accounts b ON b.id=p.bank_account_id WHERE p.id=$1::uuid`, b.ID).Scan(&ref, &amount, &bank, &status)
	if err != nil {
		c.JSON(404, gin.H{"code": "payout_not_found"})
		return
	}
	config, err := h.topups.Configured.Config(c.Request.Context(), ref)
	if err != nil {
		c.JSON(503, gin.H{"code": "monnify_unavailable"})
		return
	}
	client := service.NewMonnifyClient(config)
	var result *service.TransferResult
	switch b.Action {
	case "initiate":
		if status != "submitting" {
			c.JSON(409, gin.H{"code": "payout_not_claimed"})
			return
		}
		result, err = client.InitiateTransfer(c.Request.Context(), ref, amount, b.Account, bank, config.DisbursementSource)
	case "authorize":
		if status != "PENDING_AUTHORIZATION" && status != "OTP_EMAIL_DISPATCH_FAILED" {
			c.JSON(409, gin.H{"code": "payout_not_awaiting_authorization"})
			return
		}
		result, err = client.AuthorizeTransfer(c.Request.Context(), ref, b.Code)
	case "resend":
		if status != "PENDING_AUTHORIZATION" && status != "OTP_EMAIL_DISPATCH_FAILED" {
			c.JSON(409, gin.H{"code": "payout_not_awaiting_authorization"})
			return
		}
		if err = client.ResendTransferOTP(c.Request.Context(), ref); err != nil {
			c.JSON(503, gin.H{"code": "otp_resend_unconfirmed"})
			return
		}
		c.JSON(200, gin.H{"data": gin.H{"submitted": true}})
		return
	case "status":
		result, err = client.TransferStatus(c.Request.Context(), ref)
	default:
		c.JSON(400, gin.H{"code": "invalid_action"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "transfer_unconfirmed"})
		return
	}
	c.JSON(200, gin.H{"data": result})
}
