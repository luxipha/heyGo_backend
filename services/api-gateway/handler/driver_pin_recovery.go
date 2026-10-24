package handler

import (
	"context"
	"crypto/subtle"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"golang.org/x/crypto/bcrypt"
)

// Recovery challenges use a separate purpose and never grant a login session.
func (a *phoneAuthAPI) requestPINRecovery(c *gin.Context) {
	var body struct {
		Channel     string `json:"channel"`
		Destination string `json:"destination"`
	}
	if c.ShouldBindJSON(&body) != nil {
		driverError(c, 400, "invalid_recovery", "Enter your verified login email or phone")
		return
	}
	var destination string
	var ok bool
	switch body.Channel {
	case "email":
		destination, ok = normalizeEmail(body.Destination)
	case "phone":
		destination, ok = normalizePhone(body.Destination)
	}
	if !ok {
		driverError(c, 400, "invalid_recovery", "Enter your verified login email or international phone number")
		return
	}
	ready, err := a.channelReady(c.Request.Context(), body.Channel)
	if err != nil || !ready || len(a.pepper) < 32 {
		driverError(c, 503, "recovery_unavailable", "Code delivery is unavailable. Try again later.")
		return
	}
	var owner string
	err = a.pool.QueryRow(c.Request.Context(), `SELECT id::text FROM users WHERE active AND 'driver'=ANY(roles) AND (($1='email' AND LOWER(email)=$2 AND email_verified) OR ($1='phone' AND phone_e164=$2 AND phone_verified_at IS NOT NULL))`, body.Channel, destination).Scan(&owner)
	if err != nil && err != pgx.ErrNoRows {
		driverError(c, 503, "recovery_unavailable", "PIN recovery is unavailable")
		return
	}
	// Unlinked/unverified contacts get the same response and rate-limit accounting,
	// but no message and no usable recovery authority.
	var ownerID *string
	send := func(context.Context, string, string) error { return nil }
	if err == nil {
		ownerID = &owner
		if body.Channel == "email" {
			send = a.emailSender.SendOTP
		} else {
			send = a.smsSender.SendOTP
		}
	}
	c.Header("Cache-Control", "no-store")
	a.requestPurposeChallenge(c, body.Channel, destination, "pin_reset", ownerID, send)
}

func (a *phoneAuthAPI) resetPIN(c *gin.Context) {
	var body struct {
		Challenge string `json:"challengeId"`
		Code      string `json:"code"`
		NewPIN    string `json:"newPin"`
	}
	if c.ShouldBindJSON(&body) != nil {
		driverError(c, 400, "invalid_recovery", "Enter the code and a new four-digit PIN")
		return
	}
	id, err := uuid.Parse(body.Challenge)
	if err != nil || len(body.Code) != 6 || strings.Trim(body.Code, "0123456789") != "" || !validDocumentPIN(body.NewPIN) {
		driverError(c, 400, "invalid_recovery", "Enter the six-digit code and a new four-digit PIN")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "recovery_unavailable", "PIN recovery is unavailable")
		return
	}
	defer tx.Rollback(c.Request.Context())
	var channel, destination, hash string
	var owner *string
	var attempts int
	var expires time.Time
	var consumed *time.Time
	err = tx.QueryRow(c.Request.Context(), `SELECT channel,destination,code_hash,pin_reset_user_id::text,attempts,expires_at,consumed_at FROM auth_otp_challenges WHERE id=$1::uuid AND purpose='pin_reset' FOR UPDATE`, id).Scan(&channel, &destination, &hash, &owner, &attempts, &expires, &consumed)
	if err != nil || consumed != nil || !time.Now().Before(expires) || attempts >= otpMaxAttempts {
		driverError(c, 401, "invalid_recovery", "The recovery code is invalid or expired")
		return
	}
	expected := otpHash(a.pepper, body.Challenge, channel+":"+destination, body.Code)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(hash)) != 1 || owner == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE auth_otp_challenges SET attempts=attempts+1 WHERE id=$1::uuid`, id)
		if err == nil {
			err = tx.Commit(c.Request.Context())
		}
		if err != nil {
			driverError(c, 503, "recovery_unavailable", "PIN recovery is unavailable")
			return
		}
		driverError(c, 401, "invalid_recovery", "The recovery code is invalid or expired")
		return
	}
	var valid bool
	err = tx.QueryRow(c.Request.Context(), `SELECT active AND 'driver'=ANY(roles) AND (($2='email' AND LOWER(email)=$3 AND email_verified) OR ($2='phone' AND phone_e164=$3 AND phone_verified_at IS NOT NULL)) FROM users WHERE id=$1::uuid FOR UPDATE`, *owner, channel, destination).Scan(&valid)
	if err != nil || !valid {
		driverError(c, 401, "invalid_recovery", "The recovery code is invalid or expired")
		return
	}
	digest, ready := documentPINDigest(*owner, body.NewPIN)
	if !ready {
		driverError(c, 503, "recovery_unavailable", "PIN recovery is unavailable")
		return
	}
	next, err := bcrypt.GenerateFromPassword([]byte(digest), bcrypt.DefaultCost)
	if err != nil {
		driverError(c, 503, "recovery_unavailable", "PIN recovery is unavailable")
		return
	}
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO driver_document_pins(driver_id,pin_hash) VALUES($1::uuid,$2) ON CONFLICT(driver_id) DO UPDATE SET pin_hash=EXCLUDED.pin_hash,failed_attempts=0,locked_until=NULL,updated_at=NOW()`, *owner, string(next))
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE auth_otp_challenges SET consumed_at=NOW() WHERE consumed_at IS NULL AND ((purpose='pin_reset' AND pin_reset_user_id=$1::uuid) OR EXISTS(SELECT 1 FROM users u WHERE u.id=$1::uuid AND ((channel='email' AND destination=LOWER(u.email)) OR (channel='phone' AND destination=u.phone_e164))))`, *owner)
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE auth_pin_challenges SET consumed_at=NOW() WHERE user_id=$1::uuid AND consumed_at IS NULL`, *owner)
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE user_sessions SET revoked_at=COALESCE(revoked_at,NOW()) WHERE user_id=$1::uuid`, *owner)
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE driver_socket_sessions SET expires_at=NOW() WHERE driver_id=$1::uuid`, *owner)
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE driver_devices SET revoked_at=NOW(),updated_at=NOW() WHERE driver_id=$1::uuid`, *owner)
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO driver_notifications(driver_id,type,title,message,data,source_event_id) VALUES($1::uuid,'account_security','PIN reset','Your PIN was reset. All sessions were signed out. Contact Support if this was not you.','{}',$2)`, *owner, "pin-reset:"+id.String())
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		driverError(c, 503, "recovery_unavailable", "PIN recovery could not be confirmed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"reset": true, "signInRequired": true}})
}
