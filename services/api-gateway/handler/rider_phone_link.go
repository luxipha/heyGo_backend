package handler

import (
	"crypto/subtle"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/httpmiddleware"
	"github.com/luxipha/heyGo_backend/shared/rideridentity"
	"net/http"
	"strings"
	"time"
)

type riderPhoneAPI struct{ auth phoneAuthAPI }

func newRiderPhoneAPI(pool *pgxpool.Pool, cfg PhoneAuthConfig) *riderPhoneAPI {
	sender := cfg.Sender
	if sender == nil {
		sender = &configuredKudiSMSSender{pool: pool, key: cfg.ResendSecretKey}
	}
	return &riderPhoneAPI{auth: phoneAuthAPI{pool: pool, pepper: cfg.OTPPepper, smsSender: sender, deliveryKey: cfg.ResendSecretKey}}
}
func registerRiderPhoneRoutes(g *gin.RouterGroup, pool *pgxpool.Pool, cfg PhoneAuthConfig) {
	a := newRiderPhoneAPI(pool, cfg)
	r := g.Group("/rider/phone", gatewayauth.RequireRole("rider"))
	r.Use(func(c *gin.Context) { c.Header("Cache-Control", "private, no-store"); c.Next() })
	r.GET("", a.read)
	limited := r.Group("", httpmiddleware.NewRateLimiter(10, time.Minute).Middleware)
	limited.POST("/request", a.request)
	limited.POST("/confirm", a.confirm)
	g.GET("/rider/booking-eligibility", gatewayauth.RequireRole("rider"), a.eligibility)
}

func (a *riderPhoneAPI) read(c *gin.Context) {
	var phone string
	var verified bool
	if err := a.auth.pool.QueryRow(c.Request.Context(), `SELECT COALESCE(phone_e164,''),phone_verified_at IS NOT NULL AND COALESCE(phone_e164,'')<>'' FROM users WHERE id=$1::uuid AND active`, riderAccountOwner(c)).Scan(&phone, &verified); err != nil {
		driverError(c, 503, "phone_unavailable", "Phone details are unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"phone": phone, "verified": verified}})
}
func (a *riderPhoneAPI) request(c *gin.Context) {
	var body struct {
		Phone string `json:"phone"`
	}
	if c.ShouldBindJSON(&body) != nil {
		driverError(c, 400, "invalid_phone", "Enter a Nigerian phone number")
		return
	}
	phone, ok := normalizePhone(body.Phone)
	if !ok || !strings.HasPrefix(phone, "+234") || len(phone) != 14 {
		driverError(c, 400, "invalid_phone", "Enter a Nigerian +234 phone number")
		return
	}
	ready, err := a.auth.channelReady(c.Request.Context(), "phone")
	if err != nil || !ready || len(a.auth.pepper) < 32 {
		driverError(c, 503, "sms_unavailable", "SMS verification is unavailable. Please retry later.")
		return
	}
	tx, err := a.auth.pool.Begin(c.Request.Context())
	if err != nil {
		phoneLinkFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var current string
	var verified bool
	if err = tx.QueryRow(c.Request.Context(), `SELECT COALESCE(phone_e164,''),phone_verified_at IS NOT NULL FROM users WHERE id=$1::uuid AND active AND 'rider'=ANY(roles) FOR UPDATE`, riderAccountOwner(c)).Scan(&current, &verified); err != nil {
		phoneLinkFailure(c)
		return
	}
	if verified && current != "" {
		driverError(c, 409, "phone_already_verified", "Your phone is already verified. Contact Support to change it.")
		return
	}
	var conflict bool
	if err = tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE phone_e164=$1 AND id<>$2::uuid) OR EXISTS(SELECT 1 FROM deactivated_phone_identities WHERE phone_hash=$3)`, phone, riderAccountOwner(c), gatewayauth.TokenHash(phone)).Scan(&conflict); err != nil {
		phoneLinkFailure(c)
		return
	}
	if conflict {
		driverError(c, 409, "phone_in_use", "This number cannot be linked. Contact Support if it belongs to you.")
		return
	}
	if !reserveOTPBudget(c, tx, "phone", riderAccountOwner(c), phone) {
		return
	}
	code, err := newOTPCode()
	if err != nil {
		phoneLinkFailure(c)
		return
	}
	id := uuid.NewString()
	expires := time.Now().UTC().Add(otpLifetime)
	if _, err = tx.Exec(c.Request.Context(), `INSERT INTO rider_phone_challenges(id,rider_id,destination,previous_phone,code_hash,expires_at) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6)`, id, riderAccountOwner(c), phone, current, otpHash(a.auth.pepper, id, "rider_phone:"+phone, code), expires); err != nil {
		phoneLinkFailure(c)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		phoneLinkFailure(c)
		return
	}
	if err = a.auth.smsSender.SendOTP(c.Request.Context(), phone, code); err != nil {
		_, _ = a.auth.pool.Exec(c.Request.Context(), `UPDATE rider_phone_challenges SET consumed_at=NOW() WHERE id=$1::uuid`, id)
		driverError(c, 502, "sms_delivery_failed", "The code could not be sent. Please retry later.")
		return
	}
	c.JSON(http.StatusAccepted, contracts.APIResponse{Data: gin.H{"challengeId": id, "expiresAt": expires}})
}
func (a *riderPhoneAPI) confirm(c *gin.Context) {
	var body struct {
		Challenge string `json:"challengeId"`
		Code      string `json:"code"`
	}
	if c.ShouldBindJSON(&body) != nil || len(body.Code) != 6 || strings.Trim(body.Code, "0123456789") != "" {
		driverError(c, 400, "invalid_code", "Enter the six-digit code")
		return
	}
	id, err := uuid.Parse(body.Challenge)
	if err != nil {
		driverError(c, 400, "invalid_code", "Invalid verification challenge")
		return
	}
	if len(a.auth.pepper) < 32 {
		driverError(c, 503, "sms_unavailable", "Phone verification is unavailable")
		return
	}
	tx, err := a.auth.pool.Begin(c.Request.Context())
	if err != nil {
		phoneLinkFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var current string
	var verified bool
	if err = tx.QueryRow(c.Request.Context(), `SELECT COALESCE(phone_e164,''),phone_verified_at IS NOT NULL FROM users WHERE id=$1::uuid AND active AND 'rider'=ANY(roles) FOR UPDATE`, riderAccountOwner(c)).Scan(&current, &verified); err != nil {
		phoneLinkFailure(c)
		return
	}
	var phone, previous, hash string
	var expires time.Time
	var consumed *time.Time
	var attempts int
	err = tx.QueryRow(c.Request.Context(), `SELECT destination,previous_phone,code_hash,expires_at,consumed_at,attempts FROM rider_phone_challenges WHERE id=$1::uuid AND rider_id=$2::uuid FOR UPDATE`, id, riderAccountOwner(c)).Scan(&phone, &previous, &hash, &expires, &consumed, &attempts)
	if err != nil || consumed != nil || !time.Now().Before(expires) || attempts >= otpMaxAttempts {
		driverError(c, 401, "invalid_code", "The code is invalid or expired")
		return
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(otpHash(a.auth.pepper, body.Challenge, "rider_phone:"+phone, body.Code))) != 1 {
		if _, err = tx.Exec(c.Request.Context(), `UPDATE rider_phone_challenges SET attempts=attempts+1 WHERE id=$1::uuid`, id); err != nil {
			phoneLinkFailure(c)
			return
		}
		if err = tx.Commit(c.Request.Context()); err != nil {
			phoneLinkFailure(c)
			return
		}
		driverError(c, 401, "invalid_code", "The code is invalid or expired")
		return
	}
	if current != previous || (verified && current != "") {
		driverError(c, 409, "phone_changed", "Phone details changed. Refresh and try again.")
		return
	}
	var conflict bool
	if err = tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE phone_e164=$1 AND id<>$2::uuid) OR EXISTS(SELECT 1 FROM deactivated_phone_identities WHERE phone_hash=$3)`, phone, riderAccountOwner(c), gatewayauth.TokenHash(phone)).Scan(&conflict); err != nil {
		phoneLinkFailure(c)
		return
	}
	if conflict {
		driverError(c, 409, "phone_in_use", "This number cannot be linked. Contact Support.")
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `UPDATE users SET phone_e164=$2,phone_verified_at=NOW(),updated_at=NOW() WHERE id=$1::uuid`, riderAccountOwner(c), phone); err != nil {
		driverError(c, 409, "phone_in_use", "This number cannot be linked. Contact Support.")
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `UPDATE rider_phone_challenges SET consumed_at=NOW() WHERE rider_id=$1::uuid AND consumed_at IS NULL`, riderAccountOwner(c)); err != nil {
		phoneLinkFailure(c)
		return
	}
	if _, err = insertDriverEvent(c, tx, riderAccountOwner(c), "driver.phone.verified", gin.H{"verified": true, "last4": phone[len(phone)-4:]}); err != nil {
		phoneLinkFailure(c)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		phoneLinkFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"phone": phone, "verified": true}})
}

func (a *riderPhoneAPI) eligibility(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	state, e := rideridentity.Read(c.Request.Context(), a.auth.pool, riderAccountOwner(c))
	if e != nil {
		driverError(c, 503, "identity_unavailable", "Contact details are unavailable")
		return
	}
	missing := []string{}
	if !state.EmailVerified {
		missing = append(missing, "email")
	}
	if !state.PhoneVerified {
		missing = append(missing, "phone")
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"ready": len(missing) == 0, "emailVerified": state.EmailVerified, "phoneVerified": state.PhoneVerified, "missing": missing}})
}
