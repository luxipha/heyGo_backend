package handler

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type reviewOTPSettings struct {
	Enabled    bool      `json:"enabled"`
	Email      string    `json:"email"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Version    int64     `json:"version"`
	Ciphertext []byte    `json:"-"`
}

func (s reviewOTPSettings) active() bool {
	return s.Enabled && s.ExpiresAt.After(time.Now().Add(2*time.Second)) && len(s.Ciphertext) > 0 && s.Email != ""
}

type reviewOTPReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readReviewOTP(ctx context.Context, db reviewOTPReader) (reviewOTPSettings, error) {
	var s reviewOTPSettings
	err := db.QueryRow(ctx, `SELECT enabled,email,expires_at,version,code_ciphertext FROM admin_review_otp WHERE id=1`).Scan(&s.Enabled, &s.Email, &s.ExpiresAt, &s.Version, &s.Ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	return s, err
}
func (a *adminAPI) getReviewOTP(c *gin.Context) {
	s, err := readReviewOTP(c.Request.Context(), a.pool)
	if err != nil {
		driverError(c, 503, "review_otp_unavailable", "Review sign-in settings are unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"settings": s, "codeConfigured": len(s.Ciphertext) > 0, "encryptionReady": len(a.settingsKey) == 32, "active": s.active()}})
}
func (a *adminAPI) putReviewOTP(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var b struct {
		Enabled   bool      `json:"enabled"`
		Email     string    `json:"email"`
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expiresAt"`
		Version   int64     `json:"version"`
		Reason    string    `json:"reason"`
	}
	if c.ShouldBindJSON(&b) != nil || len(strings.TrimSpace(b.Reason)) < 3 || len(b.Reason) > 500 {
		driverError(c, 400, "invalid_review_otp", "Valid review settings and a reason are required")
		return
	}
	var valid bool
	b.Email, valid = normalizeEmail(b.Email)
	if !valid || (b.Code != "" && !regexp.MustCompile(`^\d{6}$`).MatchString(b.Code)) || (b.Enabled && (!b.ExpiresAt.After(time.Now()) || b.ExpiresAt.After(time.Now().Add(90*24*time.Hour)))) {
		driverError(c, 400, "invalid_review_otp", "Provide one review email, a six-digit code and an expiry within 90 days")
		return
	}
	if len(a.settingsKey) != 32 {
		driverError(c, 503, "settings_encryption_unavailable", "Settings encryption is unavailable")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "review_otp_unavailable", "Review settings could not be saved")
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(680001)`); err != nil {
		driverError(c, 503, "review_otp_unavailable", "Review settings could not be saved")
		return
	}
	old, err := readReviewOTP(c.Request.Context(), tx)
	if err != nil {
		driverError(c, 503, "review_otp_unavailable", "Review settings could not be read")
		return
	}
	if old.Version != b.Version {
		driverError(c, 409, "review_otp_changed", "Reload review settings before saving")
		return
	}
	sealed := old.Ciphertext
	if b.Code != "" {
		sealed, err = sealAdminValue(a.settingsKey, b.Code, "heygo-review-otp-v1")
	}
	if b.Enabled && len(sealed) == 0 {
		driverError(c, 400, "review_code_required", "Set the review code before enabling")
		return
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO admin_review_otp(id,enabled,email,expires_at,version,code_ciphertext,updated_by) VALUES(1,$1,$2,$3,1,$4,$5::UUID) ON CONFLICT(id) DO UPDATE SET enabled=EXCLUDED.enabled,email=EXCLUDED.email,expires_at=EXCLUDED.expires_at,version=admin_review_otp.version+1,code_ciphertext=EXCLUDED.code_ciphertext,updated_by=EXCLUDED.updated_by,updated_at=NOW()`, b.Enabled, b.Email, b.ExpiresAt, sealed, currentAdmin(c).ID)
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE user_sessions SET revoked_at=NOW() WHERE review_otp_version IS NOT NULL AND revoked_at IS NULL`)
	}
	if err == nil {
		err = adminAudit(c, tx, "review_otp.settings.updated", "", gin.H{"enabled": b.Enabled, "expiresAt": b.ExpiresAt, "codeChanged": b.Code != "", "reason": strings.TrimSpace(b.Reason), "previousVersion": old.Version})
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		driverError(c, 503, "review_otp_unavailable", "Review settings could not be saved")
		return
	}
	a.getReviewOTP(c)
}
