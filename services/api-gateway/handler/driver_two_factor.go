package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func (a *driverAPI) twoFactorStatus(c *gin.Context) {
	var enabled, configured bool
	err := a.pool.QueryRow(c.Request.Context(), `SELECT driver_two_factor_enabled,COALESCE((SELECT pin_hash<>'' FROM driver_document_pins WHERE driver_id=users.id),FALSE) FROM users WHERE id=$1::uuid`, driverID(c)).Scan(&enabled, &configured)
	if err != nil {
		driverError(c, 503, "security_unavailable", "Security settings unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"enabled": enabled, "pinConfigured": configured}})
}

func (a *driverAPI) setTwoFactor(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var body struct {
		Enabled *bool   `json:"enabled"`
		PIN     string  `json:"pin"`
		NewPIN  *string `json:"newPin"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Enabled == nil || (!*body.Enabled && body.NewPIN != nil) {
		driverError(c, 400, "invalid_security_settings", "Confirm your PIN to change two-factor authentication")
		return
	}
	owner := driverID(c)
	// First-time enrollment can create a PIN. Existing PINs must be confirmed;
	// an enrollment request never silently replaces an existing PIN.
	if body.NewPIN != nil {
		var configured bool
		if err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT pin_hash<>'' FROM driver_document_pins WHERE driver_id=$1::uuid),FALSE)`, owner).Scan(&configured); err != nil {
			reviewFailure(c)
			return
		}
		if configured {
			driverError(c, 409, "pin_already_configured", "Confirm your existing PIN")
			return
		}
	}
	if !a.authenticatePINFor(c, owner, body.PIN, body.NewPIN, func(tx pgx.Tx) error {
		_, err := tx.Exec(c.Request.Context(), `UPDATE users SET driver_two_factor_enabled=$2,updated_at=NOW() WHERE id=$1::uuid`, owner, *body.Enabled)
		if err == nil {
			_, err = tx.Exec(c.Request.Context(), `INSERT INTO driver_notifications(driver_id,type,title,message,data,source_event_id) VALUES($1::uuid,'account_security','Security settings updated',$2,'{}',$3)`, owner, func() string {
				if *body.Enabled {
					return "Two-factor authentication is enabled. Future sign-ins require your OTP and PIN."
				}
				return "Two-factor authentication is disabled."
			}(), "two-factor:"+uuid.NewString())
		}
		return err
	}) {
		return
	}
	a.twoFactorStatus(c)
}

func (a *phoneAuthAPI) verifyLoginPIN(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var body struct {
		Challenge string `json:"challengeToken"`
		PIN       string `json:"pin"`
	}
	if c.ShouldBindJSON(&body) != nil || len(body.Challenge) != 43 || !validDocumentPIN(body.PIN) {
		driverError(c, 400, "invalid_pin_challenge", "Enter your four-digit PIN")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var owner, device string
	var version *int64
	var reviewExpiry *time.Time
	err = tx.QueryRow(c.Request.Context(), `SELECT user_id::text,device_name,review_otp_version,review_expires_at FROM auth_pin_challenges WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>NOW() FOR UPDATE`, gatewayauth.TokenHash(body.Challenge)).Scan(&owner, &device, &version, &reviewExpiry)
	if err != nil {
		if err == pgx.ErrNoRows {
			driverError(c, 401, "invalid_pin_challenge", "Sign-in expired. Request a new code.")
		} else {
			reviewFailure(c)
		}
		return
	}
	if version != nil {
		if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(680001)`); err != nil {
			reviewFailure(c)
			return
		}
		r, e := readReviewOTP(c.Request.Context(), tx)
		if e != nil || !r.active() || r.Version != *version || reviewExpiry == nil || !reviewExpiry.After(time.Now()) {
			driverError(c, 401, "invalid_pin_challenge", "Sign-in expired. Request a new code.")
			return
		}
	}
	user, err := a.users.ByID(c.Request.Context(), owner)
	if err != nil {
		driverError(c, 401, "authentication_required", "Sign-in is unavailable")
		return
	}
	pinAPI := driverAPI{pool: a.pool}
	if !pinAPI.authenticatePINFor(c, owner, body.PIN, nil, nil) {
		return
	}
	result, err := tx.Exec(c.Request.Context(), `UPDATE auth_pin_challenges SET consumed_at=clock_timestamp() WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>clock_timestamp()`, gatewayauth.TokenHash(body.Challenge))
	if err != nil {
		reviewFailure(c)
		return
	}
	if result.RowsAffected() != 1 {
		driverError(c, 401, "invalid_pin_challenge", "Sign-in expired. Request a new code.")
		return
	}
	tokens, err := insertSession(c.Request.Context(), tx, owner, device)
	if err == nil && version != nil {
		if reviewExpiry.Add(-time.Second).Before(tokens.accessExpires) {
			tokens.accessExpires = reviewExpiry.Add(-time.Second)
		}
		if reviewExpiry.Before(tokens.refreshExpires) {
			tokens.refreshExpires = *reviewExpiry
		}
		_, err = tx.Exec(c.Request.Context(), `UPDATE user_sessions SET review_otp_version=$2,access_expires_at=$3,refresh_expires_at=$4 WHERE access_token_hash=$1`, gatewayauth.TokenHash(tokens.access), version, tokens.accessExpires, tokens.refreshExpires)
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		reviewFailure(c)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"accessToken": tokens.access, "refreshToken": tokens.refresh, "accessExpiresAt": tokens.accessExpires, "refreshExpiresAt": tokens.refreshExpires, "user": user}})
}
