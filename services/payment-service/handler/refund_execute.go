package handler

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *WebhookHandler) refundInternalAuthorized(c *gin.Context) bool {
	if h.topups == nil || h.topups.InternalToken == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Internal-Service-Token")), []byte(h.topups.InternalToken)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": "internal_auth_required"})
		return false
	}
	if h.topups.RefundExecutor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "refund_provider_unavailable"})
		return false
	}
	return true
}

func (h *WebhookHandler) initiateRefund(c *gin.Context) {
	if !h.refundInternalAuthorized(c) {
		return
	}
	var body struct {
		TransactionReference string `json:"transactionReference"`
		RefundReference      string `json:"refundReference"`
		AmountKobo           int64  `json:"amountKobo"`
		Reason               string `json:"reason"`
		AccountNumber        string `json:"accountNumber"`
		BankCode             string `json:"bankCode"`
	}
	if c.ShouldBindJSON(&body) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_refund"})
		return
	}
	result, err := h.topups.RefundExecutor.InitiateRefund(c.Request.Context(), body.TransactionReference, body.RefundReference,
		body.AmountKobo, body.Reason, body.AccountNumber, body.BankCode)
	if err != nil {
		// The provider may have accepted a request whose HTTP response was lost.
		// The caller must reconcile by refundReference, not blindly retry.
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "refund_execution_uncertain"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func (h *WebhookHandler) refundStatus(c *gin.Context) {
	if !h.refundInternalAuthorized(c) {
		return
	}
	var body struct {
		RefundReference string `json:"refundReference"`
	}
	if c.ShouldBindJSON(&body) != nil || body.RefundReference == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_refund_reference"})
		return
	}
	result, err := h.topups.RefundExecutor.RefundStatus(c.Request.Context(), body.RefundReference)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "refund_status_unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}
