package handler

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

const resendEmailEndpoint = "https://api.resend.com/emails"

type resendSettings struct {
	APIKeyCiphertext []byte
	FromEmail        string
	FromName         string
	UpdatedAt        time.Time
}

func registerAdminPrivacyRoutes(group *gin.RouterGroup, api *adminAPI) {
	group.GET("/settings/resend", api.getResendSettings)
	group.PUT("/settings/resend", api.requireCSRF, api.putResendSettings)
	group.POST("/settings/resend/test", api.requireCSRF, api.testResendSettings)
	group.GET("/account-deletion-requests", api.listAccountDeletionRequests)
	group.POST("/account-deletion-requests/:requestID/review", api.requireCSRF, api.reviewAccountDeletionRequest)
}

func sealAdminSecret(key []byte, secret string) ([]byte, error) {
	return sealAdminValue(key, secret, "heygo-resend-api-key-v1")
}

func sealAdminValue(key []byte, secret, purpose string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return append(nonce, aead.Seal(nil, nonce, []byte(secret), []byte(purpose))...), nil
}

func openAdminSecret(key, sealed []byte) (string, error) {
	return openAdminValue(key, sealed, "heygo-resend-api-key-v1")
}

func openAdminValue(key, sealed []byte, purpose string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return "", fmt.Errorf("encrypted secret is malformed")
	}
	nonce, ciphertext := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(purpose))
	if err != nil {
		return "", fmt.Errorf("decrypt admin email secret: %w", err)
	}
	return string(plaintext), nil
}

func validResendEmail(value string) bool {
	parsed, err := mail.ParseAddress(strings.TrimSpace(value))
	return err == nil && parsed.Address == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n")
}

func (a *adminAPI) readResendSettings(ctx context.Context) (resendSettings, bool, error) {
	var result resendSettings
	err := a.pool.QueryRow(ctx, `SELECT api_key_ciphertext,from_email,from_name,updated_at FROM admin_resend_settings WHERE id=1`).Scan(
		&result.APIKeyCiphertext, &result.FromEmail, &result.FromName, &result.UpdatedAt)
	if err == pgx.ErrNoRows {
		return resendSettings{}, false, nil
	}
	return result, err == nil, err
}

func (a *adminAPI) getResendSettings(ctx *gin.Context) {
	settings, configured, err := a.readResendSettings(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "resend_settings_unavailable", "Email settings are unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{
		"configured": configured, "encryptionReady": len(a.settingsKey) == 32,
		"fromEmail": settings.FromEmail, "fromName": settings.FromName, "updatedAt": settings.UpdatedAt,
	}})
}

func (a *adminAPI) putResendSettings(ctx *gin.Context) {
	if len(a.settingsKey) != 32 {
		driverError(ctx, http.StatusServiceUnavailable, "settings_encryption_unavailable", "Admin email settings encryption is not configured")
		return
	}
	var body struct {
		APIKey    string `json:"apiKey"`
		FromEmail string `json:"fromEmail"`
		FromName  string `json:"fromName"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_resend_settings", "Valid Resend settings are required")
		return
	}
	body.APIKey = strings.TrimSpace(body.APIKey)
	body.FromEmail = strings.TrimSpace(body.FromEmail)
	body.FromName = strings.TrimSpace(body.FromName)
	if !validResendEmail(body.FromEmail) || len(body.FromEmail) > 254 || len(body.FromName) > 100 || strings.ContainsAny(body.FromName, "\r\n") || (body.APIKey != "" && (len(body.APIKey) < 10 || len(body.APIKey) > 256 || strings.ContainsAny(body.APIKey, "\r\n"))) {
		driverError(ctx, http.StatusBadRequest, "invalid_resend_settings", "A valid sender email, sender name, and optional Resend API key are required")
		return
	}
	current, configured, err := a.readResendSettings(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "resend_settings_unavailable", "Email settings are unavailable")
		return
	}
	ciphertext := current.APIKeyCiphertext
	if body.APIKey != "" {
		ciphertext, err = sealAdminSecret(a.settingsKey, body.APIKey)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "resend_settings_unavailable", "Email settings could not be encrypted")
			return
		}
	}
	if !configured && len(ciphertext) == 0 {
		driverError(ctx, http.StatusBadRequest, "resend_api_key_required", "A Resend API key is required for initial setup")
		return
	}
	if body.FromName == "" {
		body.FromName = "HeyGo"
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "resend_settings_unavailable", "Email settings could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO admin_resend_settings(id,api_key_ciphertext,from_email,from_name,updated_by,updated_at)
		VALUES(1,$1,$2,$3,$4::UUID,NOW()) ON CONFLICT(id) DO UPDATE SET api_key_ciphertext=EXCLUDED.api_key_ciphertext,
		from_email=EXCLUDED.from_email,from_name=EXCLUDED.from_name,updated_by=EXCLUDED.updated_by,updated_at=NOW()`,
		ciphertext, body.FromEmail, body.FromName, currentAdmin(ctx).ID)
	if err == nil {
		err = adminAudit(ctx, tx, "privacy.resend_settings.updated", "", gin.H{"fromEmail": body.FromEmail, "fromName": body.FromName, "apiKeyChanged": body.APIKey != ""})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "resend_settings_unavailable", "Email settings could not be saved")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"configured": true, "fromEmail": body.FromEmail, "fromName": body.FromName}})
}

func (a *adminAPI) testResendSettings(ctx *gin.Context) {
	var body struct {
		To string `json:"to"`
	}
	if ctx.ShouldBindJSON(&body) != nil || !validResendEmail(body.To) {
		driverError(ctx, http.StatusBadRequest, "invalid_test_recipient", "A valid test recipient email is required")
		return
	}
	settings, configured, err := a.readResendSettings(ctx.Request.Context())
	if err != nil || !configured || len(a.settingsKey) != 32 {
		driverError(ctx, http.StatusConflict, "resend_not_configured", "Configure Resend before sending a test email")
		return
	}
	key, err := openAdminSecret(a.settingsKey, settings.APIKeyCiphertext)
	if err == nil {
		err = sendResendEmail(ctx.Request.Context(), key, settings.FromEmail, settings.FromName, body.To,
			"HeyGo email setup test", "This is a test email from the HeyGo Admin settings.", "")
	}
	if err != nil {
		driverError(ctx, http.StatusBadGateway, "resend_test_failed", "Resend could not send the test email")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"sent": true, "to": body.To}})
}

func sendResendEmail(ctx context.Context, apiKey, fromEmail, fromName, to, subject, textBody, htmlBody string) error {
	return sendResendEmailWithKey(ctx, "", apiKey, fromEmail, fromName, to, subject, textBody, htmlBody)
}

func sendResendEmailWithKey(ctx context.Context, idempotencyKey, apiKey, fromEmail, fromName, to, subject, textBody, htmlBody string) error {
	return sendResendEmailTo(ctx, resendEmailEndpoint, idempotencyKey, apiKey, fromEmail, fromName, to, subject, textBody, htmlBody, &http.Client{Timeout: 10 * time.Second})
}

func sendResendEmailTo(ctx context.Context, endpoint, idempotencyKey, apiKey, fromEmail, fromName, to, subject, textBody, htmlBody string, client *http.Client) error {
	from := fromEmail
	if fromName != "" {
		from = fromName + " <" + fromEmail + ">"
	}
	body, err := json.Marshal(gin.H{"from": from, "to": []string{to}, "subject": subject, "text": textBody, "html": htmlBody})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("send Resend message: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Resend returned status %d", response.StatusCode)
	}
	return nil
}
