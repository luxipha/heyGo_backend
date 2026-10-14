package handler

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func (a *adminAPI) getEmailDelivery(ctx *gin.Context) {
	s, err := readEmailDeliverySettings(ctx.Request.Context(), a.pool)
	if err != nil {
		driverError(ctx, 503, "email_settings_unavailable", "Email settings are unavailable")
		return
	}
	ctx.JSON(200, contracts.APIResponse{Data: gin.H{"settings": s, "smtpConfigured": s.Host != "" && validResendEmail(s.FromEmail), "passwordConfigured": len(s.Ciphertext) > 0, "encryptionReady": len(a.settingsKey) == 32}})
}
func validSMTPSettings(s emailDeliverySettings) bool {
	if s.PrimaryProvider != "resend" && s.PrimaryProvider != "smtp" {
		return false
	}
	if s.Port < 1 || s.Port > 65535 || (s.TLSMode != "starttls" && s.TLSMode != "tls") {
		return false
	}
	if strings.ContainsAny(s.Host, " /\\\r\n\t") || len(s.Host) > 253 || len(s.Username) > 254 || len(s.Password) > 1024 || len(s.FromName) > 100 || strings.ContainsAny(s.Username+s.FromName, "\r\n") {
		return false
	}
	if s.FromEmail != "" && !validResendEmail(s.FromEmail) {
		return false
	}
	if s.SMTPEnabled && (s.Host == "" || !validResendEmail(s.FromEmail)) {
		return false
	}
	return true
}
func (a *adminAPI) putEmailDelivery(ctx *gin.Context) {
	if len(a.settingsKey) != 32 {
		driverError(ctx, 503, "settings_encryption_unavailable", "Email settings encryption is unavailable")
		return
	}
	var s emailDeliverySettings
	if ctx.ShouldBindJSON(&s) != nil || !validSMTPSettings(s) {
		driverError(ctx, 400, "invalid_email_settings", "Valid email provider settings and TLS-enabled SMTP configuration are required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, 503, "email_settings_unavailable", "Email settings could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	// Serialize settings writers, including initial creation, and preserve blank write-only secrets.
	_, err = tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(620001)`)
	if err == nil {
		err = tx.QueryRow(ctx.Request.Context(), `SELECT smtp_password_ciphertext FROM admin_email_delivery_settings WHERE id=1`).Scan(&s.Ciphertext)
		if err != nil && errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
	}
	if err == nil && s.Password != "" {
		s.Ciphertext, err = sealAdminValue(a.settingsKey, s.Password, "heygo-smtp-password-v1")
	}
	if err == nil && s.SMTPEnabled && s.Username != "" && len(s.Ciphertext) == 0 {
		driverError(ctx, 400, "smtp_password_required", "A password is required for authenticated SMTP")
		return
	}
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO admin_email_delivery_settings(id,resend_enabled,smtp_enabled,primary_provider,fallback_enabled,smtp_host,smtp_port,smtp_tls_mode,smtp_username,smtp_password_ciphertext,smtp_from_email,smtp_from_name,updated_by) VALUES(1,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::UUID) ON CONFLICT(id) DO UPDATE SET resend_enabled=EXCLUDED.resend_enabled,smtp_enabled=EXCLUDED.smtp_enabled,primary_provider=EXCLUDED.primary_provider,fallback_enabled=EXCLUDED.fallback_enabled,smtp_host=EXCLUDED.smtp_host,smtp_port=EXCLUDED.smtp_port,smtp_tls_mode=EXCLUDED.smtp_tls_mode,smtp_username=EXCLUDED.smtp_username,smtp_password_ciphertext=EXCLUDED.smtp_password_ciphertext,smtp_from_email=EXCLUDED.smtp_from_email,smtp_from_name=EXCLUDED.smtp_from_name,updated_by=EXCLUDED.updated_by,updated_at=NOW()`, s.ResendEnabled, s.SMTPEnabled, s.PrimaryProvider, s.FallbackEnabled, s.Host, s.Port, s.TLSMode, s.Username, s.Ciphertext, s.FromEmail, s.FromName, currentAdmin(ctx).ID)
	}
	if err == nil {
		err = adminAudit(ctx, tx, "email.delivery_settings.updated", "", gin.H{"resendEnabled": s.ResendEnabled, "smtpEnabled": s.SMTPEnabled, "primaryProvider": s.PrimaryProvider, "fallbackEnabled": s.FallbackEnabled, "passwordChanged": s.Password != ""})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, 503, "email_settings_unavailable", "Email settings could not be saved")
		return
	}
	a.getEmailDelivery(ctx)
}
func (a *adminAPI) testEmailDelivery(ctx *gin.Context) {
	var b struct {
		To string `json:"to"`
	}
	if ctx.ShouldBindJSON(&b) != nil || !validResendEmail(b.To) {
		driverError(ctx, 400, "invalid_email", "A valid test recipient is required")
		return
	}
	err := sendConfiguredEmail(ctx.Request.Context(), a.pool, a.settingsKey, b.To, "HeyGo email delivery test", "Your configured HeyGo email delivery route accepted this test request.", "")
	if err != nil {
		driverError(ctx, 502, "email_test_failed", "Email was not confirmed accepted; check provider settings before retrying")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err == nil {
		defer tx.Rollback(ctx.Request.Context())
		err = adminAudit(ctx, tx, "email.delivery_settings.test", "", gin.H{})
		if err == nil {
			err = tx.Commit(ctx.Request.Context())
		}
	}
	if err != nil {
		driverError(ctx, 503, "email_test_audit_unavailable", "Email was accepted but the audit receipt is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"accepted": true}})
}
