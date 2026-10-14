package handler

import (
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/httpmiddleware"
)

type riderIdentityAPI struct {
	pool *pgxpool.Pool
	auth phoneAuthAPI
}

func registerRiderIdentityRoutes(g *gin.RouterGroup, pool *pgxpool.Pool, cfg PhoneAuthConfig) {
	email := cfg.EmailSender
	if email == nil {
		email = &configuredResendOTPSender{pool: pool, key: cfg.ResendSecretKey}
	}
	sms := cfg.Sender
	if sms == nil {
		sms = &configuredKudiSMSSender{pool: pool, key: cfg.ResendSecretKey}
	}
	a := &riderIdentityAPI{pool: pool, auth: phoneAuthAPI{pool: pool, pepper: cfg.OTPPepper, smsSender: sms, emailSender: email, deliveryKey: cfg.ResendSecretKey}}
	r := g.Group("/rider", gatewayauth.RequireRole("rider"))
	r.GET("/profile", a.profile)
	r.PATCH("/profile", a.updateProfile)
	limited := r.Group("", httpmiddleware.NewRateLimiter(10, time.Minute).Middleware)
	limited.POST("/identity/phone-change/request", func(c *gin.Context) { a.requestContact(c, "phone_change") })
	limited.POST("/identity/phone-change/confirm", func(c *gin.Context) { a.confirmContact(c, "phone_change") })
	limited.POST("/identity/email-link/request", func(c *gin.Context) { a.requestContact(c, "email_link") })
	limited.POST("/identity/email-link/confirm", func(c *gin.Context) { a.confirmContact(c, "email_link") })
}

func (a *riderIdentityAPI) profile(c *gin.Context) {
	var name string
	if err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT display_name FROM rider_profiles WHERE rider_id=u.id),'') FROM users u WHERE id=$1::UUID AND active=TRUE`, riderAccountOwner(c)).Scan(&name); err != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"displayName": name, "nameKind": "display", "legalNameCorrection": "support_review"}})
}

func (a *riderIdentityAPI) updateProfile(c *gin.Context) {
	var body struct {
		DisplayName string `json:"displayName"`
	}
	if c.ShouldBindJSON(&body) != nil {
		driverError(c, 400, "invalid_name", "A display name is required")
		return
	}
	body.DisplayName = strings.TrimSpace(body.DisplayName)
	if !validAccountText(body.DisplayName, 100) {
		driverError(c, 400, "invalid_name", "Use a name of 1 to 100 characters without control characters")
		return
	}
	tx, err := (&riderAccountAPI{pool: a.pool}).lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO rider_profiles(rider_id,display_name) VALUES($1::UUID,$2) ON CONFLICT(rider_id) DO UPDATE SET display_name=EXCLUDED.display_name,updated_at=NOW()`, riderAccountOwner(c), body.DisplayName)
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"displayName": body.DisplayName, "nameKind": "display"}})
}

func (a *riderIdentityAPI) requestContact(c *gin.Context, purpose string) {
	var body struct {
		Destination string `json:"destination"`
	}
	if c.ShouldBindJSON(&body) != nil {
		driverError(c, 400, "invalid_contact", "A contact destination is required")
		return
	}
	channel := "email"
	dest, ok := normalizeEmail(body.Destination)
	if purpose == "phone_change" {
		channel = "phone"
		dest, ok = normalizePhone(body.Destination)
		ok = ok && strings.HasPrefix(dest, "+234") && len(dest) == 14
	}
	if !ok {
		driverError(c, 400, "invalid_contact", "Use a valid email or Nigerian +234 phone number")
		return
	}
	ready, readinessErr := a.auth.channelReady(c.Request.Context(), channel)
	if len(a.auth.pepper) < 32 || readinessErr != nil || !ready {
		driverError(c, 503, "contact_delivery_unavailable", "Contact verification is not configured")
		return
	}
	tx, err := (&riderAccountAPI{pool: a.pool}).lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var current string
	var verified bool
	q := `SELECT COALESCE(email,''),email_verified FROM users WHERE id=$1::UUID`
	if channel == "phone" {
		q = `SELECT COALESCE(phone_e164,''),phone_verified_at IS NOT NULL FROM users WHERE id=$1::UUID`
	}
	if err = tx.QueryRow(c.Request.Context(), q, riderAccountOwner(c)).Scan(&current, &verified); err != nil {
		accountError(c)
		return
	}
	if channel == "phone" && (!verified || current == "" || current == dest) {
		driverError(c, 409, "current_phone_required", "A verified current phone and a different new phone are required; contact Support if access is lost")
		return
	}
	var unavailable bool
	q = `SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(email)=LOWER($1) AND id<>$2::UUID) OR EXISTS(SELECT 1 FROM deactivated_email_identities WHERE email_hash=$3)`
	if channel == "phone" {
		q = `SELECT EXISTS(SELECT 1 FROM users WHERE phone_e164=$1 AND id<>$2::UUID) OR EXISTS(SELECT 1 FROM deactivated_phone_identities WHERE phone_hash=$3)`
	}
	if err = tx.QueryRow(c.Request.Context(), q, dest, riderAccountOwner(c), gatewayauth.TokenHash(dest)).Scan(&unavailable); err != nil {
		accountError(c)
		return
	}
	if unavailable {
		driverError(c, 409, "contact_unavailable", "This contact cannot be linked")
		return
	}
	var count int
	if err = tx.QueryRow(c.Request.Context(), `SELECT COUNT(*) FROM rider_contact_challenges WHERE rider_id=$1::UUID AND created_at>NOW()-INTERVAL '10 minutes'`, riderAccountOwner(c)).Scan(&count); err != nil {
		accountError(c)
		return
	}
	if count >= otpRequestsPerTenMinutes {
		c.Header("Retry-After", "600")
		driverError(c, 429, "otp_rate_limited", "Please wait before requesting another code")
		return
	}
	newCode, err := newOTPCode()
	if err != nil {
		accountError(c)
		return
	}
	currentCode := ""
	var currentHash *string
	id := uuid.NewString()
	expires := time.Now().UTC().Add(otpLifetime)
	if channel == "phone" {
		currentCode, err = newOTPCode()
		if err != nil {
			accountError(c)
			return
		}
		h := otpHash(a.auth.pepper, id, purpose+":current:"+current, currentCode)
		currentHash = &h
	}
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO rider_contact_challenges(id,rider_id,purpose,current_destination,new_destination,current_code_hash,new_code_hash,expires_at) VALUES($1::UUID,$2::UUID,$3,$4,$5,$6,$7,$8)`, id, riderAccountOwner(c), purpose, current, dest, currentHash, otpHash(a.auth.pepper, id, purpose+":new:"+dest, newCode), expires)
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		accountError(c)
		return
	}
	if channel == "phone" {
		err = a.auth.smsSender.SendOTP(c.Request.Context(), current, currentCode)
		if err == nil {
			err = a.auth.smsSender.SendOTP(c.Request.Context(), dest, newCode)
		}
	} else {
		err = a.auth.emailSender.SendOTP(c.Request.Context(), dest, newCode)
	}
	if err != nil {
		_, _ = a.pool.Exec(c.Request.Context(), `UPDATE rider_contact_challenges SET consumed_at=NOW() WHERE id=$1::UUID`, id)
		driverError(c, 502, "contact_delivery_failed", "Verification codes could not be delivered; nothing was changed")
		return
	}
	c.JSON(202, contracts.APIResponse{Data: gin.H{"challengeId": id, "expiresAt": expires, "requiresCurrentCode": channel == "phone", "currentDestination": current, "newDestination": dest}})
}

func (a *riderIdentityAPI) confirmContact(c *gin.Context, purpose string) {
	if len(a.auth.pepper) < 32 {
		driverError(c, 503, "contact_verification_unavailable", "Contact verification is not configured")
		return
	}
	var body struct {
		ChallengeID string `json:"challengeId"`
		CurrentCode string `json:"currentCode"`
		NewCode     string `json:"newCode"`
	}
	if c.ShouldBindJSON(&body) != nil || len(body.NewCode) != 6 || (purpose == "phone_change" && len(body.CurrentCode) != 6) {
		driverError(c, 400, "invalid_otp", "Required six-digit verification codes are missing")
		return
	}
	if _, err := uuid.Parse(body.ChallengeID); err != nil {
		driverError(c, 400, "invalid_otp", "A valid challenge is required")
		return
	}
	tx, err := (&riderAccountAPI{pool: a.pool}).lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var current, dest, newHash string
	var currentHash *string
	var attempts int
	var expires time.Time
	var consumed *time.Time
	err = tx.QueryRow(c.Request.Context(), `SELECT current_destination,new_destination,current_code_hash,new_code_hash,attempts,expires_at,consumed_at FROM rider_contact_challenges WHERE id=$1::UUID AND rider_id=$2::UUID AND purpose=$3 FOR UPDATE`, body.ChallengeID, riderAccountOwner(c), purpose).Scan(&current, &dest, &currentHash, &newHash, &attempts, &expires, &consumed)
	if err != nil || consumed != nil || attempts >= otpMaxAttempts || !time.Now().Before(expires) {
		driverError(c, 422, "invalid_otp", "Verification is invalid or expired")
		return
	}
	good := subtle.ConstantTimeCompare([]byte(newHash), []byte(otpHash(a.auth.pepper, body.ChallengeID, purpose+":new:"+dest, body.NewCode))) == 1
	if purpose == "phone_change" {
		good = good && currentHash != nil && subtle.ConstantTimeCompare([]byte(*currentHash), []byte(otpHash(a.auth.pepper, body.ChallengeID, purpose+":current:"+current, body.CurrentCode))) == 1
	}
	if !good {
		_, _ = tx.Exec(c.Request.Context(), `UPDATE rider_contact_challenges SET attempts=attempts+1 WHERE id=$1::UUID`, body.ChallengeID)
		_ = tx.Commit(c.Request.Context())
		driverError(c, 422, "invalid_otp", "Verification is invalid or expired")
		return
	}
	// Revalidate the initiating session after acquiring the account lock.
	var sessionID string
	err = tx.QueryRow(c.Request.Context(), `SELECT id::TEXT FROM user_sessions WHERE user_id=$1::UUID AND access_token_hash=$2 AND revoked_at IS NULL AND access_expires_at>NOW() FOR UPDATE`, riderAccountOwner(c), gatewayauth.TokenHash(bearerFromHeader(c.GetHeader("Authorization")))).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 401, "authentication_required", "Sign in again before changing account identity")
		return
	}
	if err != nil {
		accountError(c)
		return
	}
	var blocked bool
	blockQuery := `SELECT EXISTS(SELECT 1 FROM deactivated_email_identities WHERE email_hash=$1)`
	if purpose == "phone_change" {
		blockQuery = `SELECT EXISTS(SELECT 1 FROM deactivated_phone_identities WHERE phone_hash=$1)`
	}
	if err = tx.QueryRow(c.Request.Context(), blockQuery, gatewayauth.TokenHash(dest)).Scan(&blocked); err != nil {
		accountError(c)
		return
	}
	if blocked {
		driverError(c, 409, "contact_unavailable", "This contact cannot be linked")
		return
	}
	q := `UPDATE users SET email=$2,email_verified=TRUE,updated_at=NOW() WHERE id=$1::UUID AND COALESCE(email,'')=$3`
	if purpose == "phone_change" {
		q = `UPDATE users SET phone_e164=$2,phone_verified_at=NOW(),updated_at=NOW() WHERE id=$1::UUID AND phone_e164=$3`
	}
	result, err := tx.Exec(c.Request.Context(), q, riderAccountOwner(c), dest, current)
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			driverError(c, 409, "contact_unavailable", "This contact cannot be linked")
		} else {
			accountError(c)
		}
		return
	}
	if result.RowsAffected() != 1 {
		driverError(c, 409, "identity_changed", "Account identity changed; request new verification codes")
		return
	}
	if purpose == "phone_change" {
		if _, err = tx.Exec(c.Request.Context(), `UPDATE user_sessions SET revoked_at=COALESCE(revoked_at,NOW()) WHERE user_id=$1::UUID`, riderAccountOwner(c)); err != nil {
			accountError(c)
			return
		}
	}
	_, err = tx.Exec(c.Request.Context(), `UPDATE rider_contact_challenges SET consumed_at=NOW() WHERE rider_id=$1::UUID AND consumed_at IS NULL`, riderAccountOwner(c))
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"updated": true, "sessionsRevoked": purpose == "phone_change", "verified": true}})
}
