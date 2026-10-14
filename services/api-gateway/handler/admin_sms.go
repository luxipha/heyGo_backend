package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"strings"
)

func (a *adminAPI) getSMSSettings(c *gin.Context) {
	s, err := readSMSSettings(c.Request.Context(), a.pool)
	if err != nil {
		driverError(c, 503, "sms_settings_unavailable", "SMS settings are unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"settings": s, "configured": s.configured(), "encryptionReady": len(a.settingsKey) == 32}})
}
func (a *adminAPI) putSMSSettings(c *gin.Context) {
	if len(a.settingsKey) != 32 {
		driverError(c, 503, "settings_encryption_unavailable", "Settings encryption is unavailable")
		return
	}
	var s smsSettings
	if c.ShouldBindJSON(&s) != nil {
		driverError(c, 400, "invalid_sms_settings", "Valid Kudi SMS settings are required")
		return
	}
	s.SenderID = strings.TrimSpace(s.SenderID)
	s.AppNameCode = strings.TrimSpace(s.AppNameCode)
	s.TemplateCode = strings.TrimSpace(s.TemplateCode)
	s.APIKey = strings.TrimSpace(s.APIKey)
	if len(s.SenderID) > 11 || len(s.AppNameCode) > 100 || len(s.TemplateCode) > 100 || len(s.APIKey) > 1024 || strings.ContainsAny(s.SenderID+s.AppNameCode+s.TemplateCode+s.APIKey, "\r\n") {
		driverError(c, 400, "invalid_sms_settings", "Valid approved Kudi sender and template codes are required")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "sms_settings_unavailable", "SMS settings could not be saved")
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(630001)`)
	if err == nil {
		err = tx.QueryRow(c.Request.Context(), `SELECT api_key_ciphertext FROM admin_sms_settings WHERE id=1`).Scan(&s.Ciphertext)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
	}
	if err == nil && s.APIKey != "" {
		s.Ciphertext, err = sealAdminValue(a.settingsKey, s.APIKey, "heygo-kudi-api-key-v1")
	}
	if err == nil && s.Enabled && !s.configured() {
		driverError(c, 400, "sms_configuration_required", "Configure the API key and approved sender, app and template codes before enabling SMS")
		return
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO admin_sms_settings(id,enabled,api_key_ciphertext,sender_id,app_name_code,template_code,updated_by) VALUES(1,$1,$2,$3,$4,$5,$6::UUID) ON CONFLICT(id) DO UPDATE SET enabled=EXCLUDED.enabled,api_key_ciphertext=EXCLUDED.api_key_ciphertext,sender_id=EXCLUDED.sender_id,app_name_code=EXCLUDED.app_name_code,template_code=EXCLUDED.template_code,updated_by=EXCLUDED.updated_by,updated_at=NOW()`, s.Enabled, s.Ciphertext, s.SenderID, s.AppNameCode, s.TemplateCode, currentAdmin(c).ID)
	}
	if err == nil {
		err = adminAudit(c, tx, "sms.settings.updated", "", gin.H{"enabled": s.Enabled, "apiKeyChanged": s.APIKey != ""})
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		driverError(c, 503, "sms_settings_unavailable", "SMS settings could not be saved")
		return
	}
	a.getSMSSettings(c)
}
