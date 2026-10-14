package handler

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func (h *WebhookHandler) verifyRefundBankAccount(c *gin.Context) {
	if h.topups == nil || h.topups.InternalToken == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Internal-Service-Token")), []byte(h.topups.InternalToken)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": "internal_auth_required"})
		return
	}
	if h.topups.BankVerifier == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "bank_verification_unavailable"})
		return
	}
	var body struct {
		AccountNumber string `json:"accountNumber"`
		BankCode      string `json:"bankCode"`
	}
	if c.ShouldBindJSON(&body) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_bank_account"})
		return
	}
	verified, err := h.topups.BankVerifier.VerifyBankAccount(c.Request.Context(), body.AccountNumber, body.BankCode)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "bank_account_unverified"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"accountName": verified.AccountName, "bankName": verified.BankName}})
}

func (h *WebhookHandler) verifyRefundSource(c *gin.Context) {
	if h.topups == nil || h.topups.InternalToken == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Internal-Service-Token")), []byte(h.topups.InternalToken)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": "internal_auth_required"})
		return
	}
	if h.topups.Verifier == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "source_verification_unavailable"})
		return
	}
	var body struct {
		TransactionReference string `json:"transactionReference"`
	}
	if c.ShouldBindJSON(&body) != nil || strings.TrimSpace(body.TransactionReference) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_transaction_reference"})
		return
	}
	verified, err := h.topups.Verifier.VerifyTransaction(c.Request.Context(), body.TransactionReference)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "source_unverified"})
		return
	}
	amountKobo, err := monnifyAmountKobo(verified.AmountPaid.String())
	if err != nil || !strings.EqualFold(verified.PaymentStatus, "PAID") || !strings.EqualFold(verified.CurrencyCode, "NGN") || !strings.EqualFold(verified.PaymentMethod, "ACCOUNT_TRANSFER") {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "source_not_refundable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"transactionReference": verified.TransactionReference, "paymentReference": verified.PaymentReference,
		"amountKobo": amountKobo, "paymentMethod": verified.PaymentMethod}})
}
