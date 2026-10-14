package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/httpmiddleware"
)

const otpLifetime = 5 * time.Minute
const otpMaxAttempts = 5
const otpRequestsPerTenMinutes = 3

type SMSSender interface {
	SendOTP(context.Context, string, string) error
}

type EmailOTPSender interface {
	SendOTP(context.Context, string, string) error
}

type HTTPSMSSender struct {
	endpoint string
	token    string
	senderID string
	client   *http.Client
}

func NewHTTPSMSSender(endpoint, token, senderID string) SMSSender {
	if strings.TrimSpace(endpoint) == "" {
		return nil
	}
	return &HTTPSMSSender{endpoint: endpoint, token: token, senderID: senderID, client: &http.Client{Timeout: 10 * time.Second}}
}

func (s *HTTPSMSSender) SendOTP(ctx context.Context, phone, code string) error {
	payload, _ := json.Marshal(gin.H{"to": phone, "code": code, "senderId": s.senderID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("sms provider returned status %d", res.StatusCode)
	}
	return nil
}

type PhoneAuthConfig struct {
	OTPPepper       []byte
	Sender          SMSSender
	EmailSender     EmailOTPSender
	ResendSecretKey []byte
}

type phoneAuthAPI struct {
	pool        *pgxpool.Pool
	users       *gatewayauth.PostgresUserStore
	pepper      []byte
	smsSender   SMSSender
	emailSender EmailOTPSender
	deliveryKey []byte
}

type configuredResendOTPSender struct {
	pool *pgxpool.Pool
	key  []byte
}

func (s *configuredResendOTPSender) SendOTP(ctx context.Context, email, code string) error {
	if len(s.key) != 32 {
		return fmt.Errorf("Resend settings encryption is not configured")
	}
	subject := "Your HeyGo sign-in code"
	textBody := "Your HeyGo sign-in code is " + code + ". It expires in 5 minutes. If you did not request this code, you can ignore this email."
	htmlBody := "<p>Your HeyGo sign-in code is <strong>" + code + "</strong>.</p><p>It expires in 5 minutes. If you did not request this code, you can ignore this email.</p>"
	return sendConfiguredEmail(ctx, s.pool, s.key, email, subject, textBody, htmlBody)
}

func registerPhoneAuthRoutes(router *gin.Engine, pool *pgxpool.Pool, users *gatewayauth.PostgresUserStore, config PhoneAuthConfig) {
	emailSender := config.EmailSender
	if emailSender == nil {
		emailSender = &configuredResendOTPSender{pool: pool, key: config.ResendSecretKey}
	}
	smsSender := config.Sender
	if smsSender == nil {
		smsSender = &configuredKudiSMSSender{pool: pool, key: config.ResendSecretKey}
	}
	api := &phoneAuthAPI{
		pool: pool, users: users, pepper: config.OTPPepper, smsSender: smsSender, deliveryKey: config.ResendSecretKey,
		emailSender: emailSender,
	}
	otpLimiter := httpmiddleware.NewRateLimiter(10, time.Minute)
	router.GET("/auth/channels", httpmiddleware.NewRateLimiter(30, time.Minute).Middleware, api.authChannels)
	router.POST("/auth/otp/request", otpLimiter.Middleware, api.requestOTP)
	router.POST("/auth/otp/verify", otpLimiter.Middleware, api.verifyOTP)
	router.POST("/auth/phone/otp/request", otpLimiter.Middleware, api.requestOTP)
	router.POST("/auth/phone/otp/verify", otpLimiter.Middleware, api.verifyOTP)
	router.POST("/auth/email/otp/request", otpLimiter.Middleware, api.requestEmailOTP)
	router.POST("/auth/email/otp/verify", otpLimiter.Middleware, api.verifyEmailOTP)
	router.POST("/auth/refresh", httpmiddleware.NewRateLimiter(30, time.Minute).Middleware, api.refresh)
}

func registerPhoneSessionRoutes(authenticated *gin.RouterGroup, pool *pgxpool.Pool) {
	authenticated.POST("/auth/logout", func(ctx *gin.Context) {
		raw := bearerFromHeader(ctx.GetHeader("Authorization"))
		if raw == "" {
			driverError(ctx, http.StatusUnauthorized, "authentication_required", "Valid HeyGo authentication is required")
			return
		}
		if _, err := pool.Exec(ctx.Request.Context(), `UPDATE user_sessions SET revoked_at=NOW() WHERE access_token_hash=$1 AND revoked_at IS NULL`, gatewayauth.TokenHash(raw)); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign out is temporarily unavailable")
			return
		}
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"signedOut": true}})
	})
	authenticated.PATCH("/auth/account", func(ctx *gin.Context) {
		var body struct {
			Email string `json:"email"`
		}
		if ctx.ShouldBindJSON(&body) != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_email", "A valid email address is required")
			return
		}
		var ok bool
		body.Email, ok = normalizeEmail(body.Email)
		if !ok {
			driverError(ctx, http.StatusBadRequest, "invalid_email", "A valid email address is required")
			return
		}
		user, _ := gatewayauth.CurrentUser(ctx)
		var verified bool
		if user.HasRole("rider") && body.Email != user.Email {
			driverError(ctx, http.StatusConflict, "email_verification_required", "Verify the new email through Rider account settings before linking it")
			return
		}
		if err := pool.QueryRow(ctx.Request.Context(), `UPDATE users SET email=$2,email_verified=CASE WHEN email=$2 THEN email_verified ELSE FALSE END,updated_at=NOW() WHERE id=$1::UUID AND active=TRUE RETURNING email_verified`, user.ID, body.Email).Scan(&verified); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "account_unavailable", "Account details could not be updated")
			return
		}
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"email": body.Email, "emailVerified": verified}})
	})
}

func (a *phoneAuthAPI) requestOTP(ctx *gin.Context) {
	ready, readinessErr := a.channelReady(ctx.Request.Context(), "phone")
	if len(a.pepper) < 32 || readinessErr != nil || !ready {
		driverError(ctx, http.StatusServiceUnavailable, "sms_unavailable", "SMS sign-in is not configured")
		return
	}
	var body struct {
		Phone string `json:"phone"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_phone", "A valid international phone number is required")
		return
	}
	phone, ok := normalizePhone(body.Phone)
	if !ok {
		driverError(ctx, http.StatusBadRequest, "invalid_phone", "A valid international phone number is required")
		return
	}
	a.requestChallenge(ctx, "phone", phone, a.smsSender.SendOTP)
}

func (a *phoneAuthAPI) requestEmailOTP(ctx *gin.Context) {
	ready, readinessErr := a.channelReady(ctx.Request.Context(), "email")
	if len(a.pepper) < 32 || readinessErr != nil || !ready {
		driverError(ctx, http.StatusServiceUnavailable, "email_unavailable", "Email sign-in is not configured")
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_email", "A valid email address is required")
		return
	}
	email, ok := normalizeEmail(body.Email)
	if !ok {
		driverError(ctx, http.StatusBadRequest, "invalid_email", "A valid email address is required")
		return
	}
	a.requestChallenge(ctx, "email", email, a.emailSender.SendOTP)
}

func (a *phoneAuthAPI) requestChallenge(ctx *gin.Context, channel, destination string, send func(context.Context, string, string) error) {
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	// Serialize requests for one destination so concurrent calls cannot bypass
	// the durable rate limit.
	if _, err := tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, channel+":"+destination); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	var recent int
	if err := tx.QueryRow(ctx.Request.Context(), `SELECT COUNT(*) FROM auth_otp_challenges WHERE channel=$1 AND destination=$2 AND created_at>NOW()-INTERVAL '10 minutes'`, channel, destination).Scan(&recent); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	if recent >= otpRequestsPerTenMinutes {
		ctx.Header("Retry-After", "600")
		driverError(ctx, http.StatusTooManyRequests, "otp_rate_limited", "Please wait before requesting another code")
		return
	}
	code, err := newOTPCode()
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	id := uuid.NewString()
	expires := time.Now().UTC().Add(otpLifetime)
	if _, err := tx.Exec(ctx.Request.Context(), `INSERT INTO auth_otp_challenges(id,channel,destination,code_hash,expires_at) VALUES($1::UUID,$2,$3,$4,$5)`, id, channel, destination, otpHash(a.pepper, id, channel+":"+destination, code), expires); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	if err := tx.Commit(ctx.Request.Context()); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	if err := send(ctx.Request.Context(), destination, code); err != nil {
		_, _ = a.pool.Exec(ctx.Request.Context(), `DELETE FROM auth_otp_challenges WHERE id=$1::UUID AND consumed_at IS NULL`, id)
		driverError(ctx, http.StatusBadGateway, channel+"_delivery_failed", "The sign-in code could not be sent")
		return
	}
	ctx.JSON(http.StatusAccepted, contracts.APIResponse{Data: gin.H{"challengeId": id, "expiresAt": expires}})
}

func (a *phoneAuthAPI) verifyOTP(ctx *gin.Context) {
	if len(a.pepper) < 32 {
		driverError(ctx, http.StatusServiceUnavailable, "sms_unavailable", "SMS sign-in is not configured")
		return
	}
	var body struct {
		ChallengeID string `json:"challengeId"`
		Phone       string `json:"phone"`
		Code        string `json:"code"`
		DeviceName  string `json:"deviceName"`
	}
	if ctx.ShouldBindJSON(&body) != nil || len(body.Code) != 6 || len(body.DeviceName) > 200 {
		driverError(ctx, http.StatusBadRequest, "invalid_otp", "Challenge, phone number, and six-digit code are required")
		return
	}
	phone, ok := normalizePhone(body.Phone)
	if !ok {
		driverError(ctx, http.StatusBadRequest, "invalid_phone", "A valid international phone number is required")
		return
	}
	a.verifyChallenge(ctx, "phone", phone, body.ChallengeID, body.Code, body.DeviceName)
}

func (a *phoneAuthAPI) verifyEmailOTP(ctx *gin.Context) {
	if len(a.pepper) < 32 {
		driverError(ctx, http.StatusServiceUnavailable, "email_unavailable", "Email sign-in is not configured")
		return
	}
	var body struct {
		ChallengeID string `json:"challengeId"`
		Email       string `json:"email"`
		Code        string `json:"code"`
		DeviceName  string `json:"deviceName"`
	}
	if ctx.ShouldBindJSON(&body) != nil || len(body.Code) != 6 || len(body.DeviceName) > 200 {
		driverError(ctx, http.StatusBadRequest, "invalid_otp", "Challenge, email, and six-digit code are required")
		return
	}
	email, ok := normalizeEmail(body.Email)
	if !ok {
		driverError(ctx, http.StatusBadRequest, "invalid_email", "A valid email address is required")
		return
	}
	a.verifyChallenge(ctx, "email", email, body.ChallengeID, body.Code, body.DeviceName)
}

func (a *phoneAuthAPI) verifyChallenge(ctx *gin.Context, channel, destination, rawChallengeID, code, deviceName string) {
	challengeID, err := uuid.Parse(rawChallengeID)
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_otp", "A valid challenge and six-digit code are required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var storedHash, storedChannel, storedDestination string
	var attempts int
	var expires time.Time
	var consumed *time.Time
	err = tx.QueryRow(ctx.Request.Context(), `SELECT channel,destination,code_hash,attempts,expires_at,consumed_at FROM auth_otp_challenges WHERE id=$1::UUID FOR UPDATE`, challengeID).Scan(&storedChannel, &storedDestination, &storedHash, &attempts, &expires, &consumed)
	if err != nil || consumed != nil || time.Now().After(expires) || attempts >= otpMaxAttempts || storedChannel != channel || storedDestination != destination {
		driverError(ctx, http.StatusUnauthorized, "invalid_otp", "The sign-in code is invalid or expired")
		return
	}
	expected := otpHash(a.pepper, rawChallengeID, channel+":"+destination, code)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(storedHash)) != 1 {
		_, _ = tx.Exec(ctx.Request.Context(), `UPDATE auth_otp_challenges SET attempts=attempts+1 WHERE id=$1::UUID`, challengeID)
		_ = tx.Commit(ctx.Request.Context())
		driverError(ctx, http.StatusUnauthorized, "invalid_otp", "The sign-in code is invalid or expired")
		return
	}
	var blocked bool
	blockQuery := `SELECT EXISTS(SELECT 1 FROM deactivated_phone_identities WHERE phone_hash=$1)`
	if channel == "email" {
		blockQuery = `SELECT EXISTS(SELECT 1 FROM deactivated_email_identities WHERE email_hash=$1)`
	}
	if err := tx.QueryRow(ctx.Request.Context(), blockQuery, gatewayauth.TokenHash(destination)).Scan(&blocked); err != nil || blocked {
		if blocked {
			driverError(ctx, http.StatusForbidden, "account_deactivated", "This HeyGo account is deactivated")
		} else {
			driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		}
		return
	}
	userID := uuid.NewString()
	if channel == "phone" {
		err = tx.QueryRow(ctx.Request.Context(), `INSERT INTO users(id,phone_e164,phone_verified_at,roles,created_at,updated_at)
			VALUES($1::UUID,$2,NOW(),'{}'::TEXT[],NOW(),NOW())
			ON CONFLICT(phone_e164) WHERE phone_e164 IS NOT NULL DO UPDATE SET phone_verified_at=COALESCE(users.phone_verified_at,NOW()),updated_at=NOW()
			WHERE users.active=TRUE RETURNING id::TEXT`, userID, destination).Scan(&userID)
	} else {
		err = tx.QueryRow(ctx.Request.Context(), `INSERT INTO users(id,email,email_verified,roles,created_at,updated_at)
			VALUES($1::UUID,$2,TRUE,'{}'::TEXT[],NOW(),NOW())
			ON CONFLICT(LOWER(email)) WHERE email IS NOT NULL DO UPDATE SET email_verified=TRUE,updated_at=NOW()
			WHERE users.active=TRUE AND (NOT ('rider'=ANY(users.roles)) OR users.email_verified=TRUE) RETURNING id::TEXT`, userID, destination).Scan(&userID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if channel == "email" {
			var active bool
			if lookupErr := tx.QueryRow(ctx.Request.Context(), `SELECT active FROM users WHERE LOWER(email)=LOWER($1)`, destination).Scan(&active); lookupErr == nil && active {
				driverError(ctx, http.StatusConflict, "email_link_required", "Sign in with your existing account and verify this email from account settings, or contact Support")
				return
			}
		}
		driverError(ctx, http.StatusForbidden, "account_deactivated", "This HeyGo account is deactivated")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	tokens, err := insertSession(ctx.Request.Context(), tx, userID, strings.TrimSpace(deviceName))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	if _, err := tx.Exec(ctx.Request.Context(), `UPDATE auth_otp_challenges SET consumed_at=NOW() WHERE id=$1::UUID`, challengeID); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	if err := tx.Commit(ctx.Request.Context()); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	user, err := a.users.ByID(ctx.Request.Context(), userID)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"accessToken": tokens.access, "refreshToken": tokens.refresh, "accessExpiresAt": tokens.accessExpires, "refreshExpiresAt": tokens.refreshExpires, "user": user}})
}

func (a *phoneAuthAPI) refresh(ctx *gin.Context) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if ctx.ShouldBindJSON(&body) != nil || len(body.RefreshToken) < 40 {
		driverError(ctx, http.StatusUnauthorized, "invalid_refresh_token", "A valid refresh token is required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Session refresh is temporarily unavailable")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var sessionID, userID, deviceName string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,user_id::TEXT,device_name FROM user_sessions
		WHERE refresh_token_hash=$1 AND revoked_at IS NULL AND refresh_expires_at>NOW() FOR UPDATE`, gatewayauth.TokenHash(body.RefreshToken)).Scan(&sessionID, &userID, &deviceName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			driverError(ctx, http.StatusUnauthorized, "invalid_refresh_token", "A valid refresh token is required")
		} else {
			driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Session refresh is temporarily unavailable")
		}
		return
	}
	tokens, err := newSessionTokens()
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Session refresh is temporarily unavailable")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE user_sessions SET access_token_hash=$2,refresh_token_hash=$3,access_expires_at=$4,refresh_expires_at=$5,last_used_at=NOW()
		WHERE id=$1::UUID`, sessionID, gatewayauth.TokenHash(tokens.access), gatewayauth.TokenHash(tokens.refresh), tokens.accessExpires, tokens.refreshExpires)
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		driverError(ctx, http.StatusServiceUnavailable, "authentication_unavailable", "Session refresh is temporarily unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"accessToken": tokens.access, "refreshToken": tokens.refresh, "accessExpiresAt": tokens.accessExpires, "refreshExpiresAt": tokens.refreshExpires}})
}

type sessionTokens struct {
	access, refresh               string
	accessExpires, refreshExpires time.Time
}

func insertSession(ctx context.Context, tx pgx.Tx, userID, deviceName string) (sessionTokens, error) {
	tokens, err := newSessionTokens()
	if err != nil {
		return sessionTokens{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO user_sessions(id,user_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at,device_name)
		VALUES($1::UUID,$2::UUID,$3,$4,$5,$6,$7)`, uuid.NewString(), userID, gatewayauth.TokenHash(tokens.access), gatewayauth.TokenHash(tokens.refresh), tokens.accessExpires, tokens.refreshExpires, deviceName)
	return tokens, err
}

func newSessionTokens() (sessionTokens, error) {
	access, err := randomToken()
	if err != nil {
		return sessionTokens{}, err
	}
	refresh, err := randomToken()
	if err != nil {
		return sessionTokens{}, err
	}
	now := time.Now().UTC()
	return sessionTokens{access: access, refresh: refresh, accessExpires: now.Add(gatewayauth.AccessTokenLifetime), refreshExpires: now.Add(gatewayauth.RefreshTokenLifetime)}, nil
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func newOTPCode() (string, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

func otpHash(pepper []byte, challengeID, phone, code string) string {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(challengeID))
	mac.Write([]byte{0})
	mac.Write([]byte(phone))
	mac.Write([]byte{0})
	mac.Write([]byte(code))
	return hex.EncodeToString(mac.Sum(nil))
}

var internationalPhone = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

func normalizePhone(raw string) (string, bool) {
	value := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(raw))
	if strings.HasPrefix(value, "0") && len(value) == 11 {
		value = "+234" + value[1:]
	} else if strings.HasPrefix(value, "234") {
		value = "+" + value
	}
	return value, internationalPhone.MatchString(value)
}

func normalizeEmail(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	parsed, err := mail.ParseAddress(value)
	return value, err == nil && parsed.Address == value && !strings.ContainsAny(value, "\r\n") && len(value) <= 254
}

func bearerFromHeader(header string) string {
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
