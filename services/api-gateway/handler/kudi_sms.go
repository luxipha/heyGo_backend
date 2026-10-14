package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const kudiOTPEndpoint = "https://my.kudisms.net/api/otp"

type smsSettings struct {
	Enabled      bool   `json:"enabled"`
	APIKey       string `json:"apiKey,omitempty"`
	SenderID     string `json:"senderId"`
	AppNameCode  string `json:"appNameCode"`
	TemplateCode string `json:"templateCode"`
	Ciphertext   []byte `json:"-"`
}

func readSMSSettings(ctx context.Context, pool *pgxpool.Pool) (smsSettings, error) {
	var s smsSettings
	err := pool.QueryRow(ctx, `SELECT enabled,api_key_ciphertext,sender_id,app_name_code,template_code FROM admin_sms_settings WHERE id=1`).Scan(&s.Enabled, &s.Ciphertext, &s.SenderID, &s.AppNameCode, &s.TemplateCode)
	if err == pgx.ErrNoRows {
		err = nil
	}
	return s, err
}
func (s smsSettings) configured() bool {
	return len(s.Ciphertext) > 0 && s.SenderID != "" && s.AppNameCode != "" && s.TemplateCode != ""
}

type configuredKudiSMSSender struct {
	pool *pgxpool.Pool
	key  []byte
}

func (s *configuredKudiSMSSender) SendOTP(ctx context.Context, phone, code string) error {
	settings, err := readSMSSettings(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("SMS configuration unavailable")
	}
	if !settings.Enabled || !settings.configured() {
		return fmt.Errorf("SMS delivery disabled or not configured")
	}
	token, err := openAdminValue(s.key, settings.Ciphertext, "heygo-kudi-api-key-v1")
	if err != nil {
		return fmt.Errorf("SMS credentials unavailable")
	}
	return sendKudiOTP(ctx, kudiOTPEndpoint, &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, settings, token, phone, code)
}
func sendKudiOTP(ctx context.Context, endpoint string, client *http.Client, s smsSettings, token, phone, code string) error {
	normalized, ok := normalizePhone(phone)
	if !ok || !strings.HasPrefix(normalized, "+234") || len(normalized) != 14 {
		return fmt.Errorf("Nigerian +234 phone number required")
	}
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return fmt.Errorf("invalid OTP")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for k, v := range map[string]string{"token": token, "senderID": s.SenderID, "recipients": strings.TrimPrefix(normalized, "+"), "otp": code, "appnamecode": s.AppNameCode, "templatecode": s.TemplateCode} {
		if err := writer.WriteField(k, v); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return fmt.Errorf("SMS request unavailable")
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("SMS acceptance unresolved")
	}
	defer res.Body.Close()
	var response struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("SMS provider rejected request")
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&response); err != nil {
		return fmt.Errorf("SMS acceptance unresolved")
	}
	if response.Status != "success" || response.ErrorCode != "000" {
		return fmt.Errorf("SMS provider rejected request")
	}
	return nil // Acceptance is not handset delivery.
}
func authSMSReady(ctx context.Context, pool *pgxpool.Pool, key []byte) (bool, error) {
	s, err := readSMSSettings(ctx, pool)
	if err != nil {
		return false, err
	}
	if !s.Enabled || !s.configured() || len(key) != 32 {
		return false, nil
	}
	_, err = openAdminValue(key, s.Ciphertext, "heygo-kudi-api-key-v1")
	return err == nil, nil
}
func authEmailReady(ctx context.Context, pool *pgxpool.Pool, key []byte) (bool, error) {
	if len(key) != 32 {
		return false, nil
	}
	s, err := readEmailDeliverySettings(ctx, pool)
	if err != nil {
		return false, err
	}
	resend := false
	smtp := false
	if s.ResendEnabled {
		r, ok, e := readResendSettingsPool(ctx, pool)
		if e != nil {
			return false, e
		}
		if ok {
			_, e = openAdminSecret(key, r.APIKeyCiphertext)
			resend = e == nil && validResendEmail(r.FromEmail)
		}
	}
	if s.SMTPEnabled && s.Host != "" && validResendEmail(s.FromEmail) {
		smtp = s.Username == ""
		if s.Username != "" && len(s.Ciphertext) > 0 {
			_, e := openAdminValue(key, s.Ciphertext, "heygo-smtp-password-v1")
			smtp = e == nil
		}
	}
	if s.PrimaryProvider == "smtp" {
		return smtp || (s.FallbackEnabled && resend), nil
	}
	return resend || (s.FallbackEnabled && smtp), nil
}
