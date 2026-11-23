package handler

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	ownerCodeDigits       = 6
	ownerCodeHeroPerHour  = 15
	ownerConfirmationFor  = 30 * time.Minute
	ownerCodeResendSecond = 60
)

func newOwnerCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func maskDestination(channel, destination string) string {
	if channel == "email" {
		at := strings.Index(destination, "@")
		if at > 1 {
			return destination[:1] + "***" + destination[at:]
		}
		return "***"
	}
	if len(destination) < 4 {
		return "••••"
	}
	return "•••• " + destination[len(destination)-4:]
}

// issueOwnerCode records one code and sends it. It reuses the shared one-time-code budget,
// so a number (or email) cannot be flooded no matter which Hero asks. Returns the
// challenge id, or "" after writing an error response.
func (a *heroesAPI) issueOwnerCode(c *gin.Context, channel, destination string, send func(context.Context, string, string) error) (string, bool) {
	ctx := c.Request.Context()
	code, err := newOwnerCode()
	if err != nil {
		heroFailure(c)
		return "", false
	}
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		heroFailure(c)
		return "", false
	}
	defer tx.Rollback(ctx)
	if !reserveOTPBudget(c, tx, channel, "", destination) {
		return "", false
	}
	id := uuid.NewString()
	if _, err = tx.Exec(ctx, `INSERT INTO auth_otp_challenges(id,channel,destination,code_hash,expires_at,purpose)VALUES($1::uuid,$2,$3,$4,$5,'venue_owner')`,
		id, channel, destination, otpHash(a.pepper, id, channel+":"+destination, code), time.Now().UTC().Add(otpLifetime)); err != nil {
		heroFailure(c)
		return "", false
	}
	if _, err = tx.Exec(ctx, `INSERT INTO hero_owner_code_requests(challenge_id,hero_id)VALUES($1::uuid,$2::uuid)`, id, heroID(c)); err != nil {
		heroFailure(c)
		return "", false
	}
	if err = tx.Commit(ctx); err != nil {
		heroFailure(c)
		return "", false
	}
	// The challenge stays even if sending fails: a timeout can follow provider acceptance,
	// and the send budget must not be refundable.
	if err = send(ctx, destination, code); err != nil {
		return id, false
	}
	return id, true
}

func (a *heroesAPI) sendOwnerCode(c *gin.Context, sms SMSSender) {
	var b struct {
		Phone string `json:"phone"`
		Email string `json:"email"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, http.StatusBadRequest, "invalid_owner_contact", "Enter the owner's phone number")
		return
	}
	phone, okPhone := normalizePhone(b.Phone)
	email := ""
	if strings.TrimSpace(b.Email) != "" {
		e, ok := heroEmail(b.Email)
		if !ok {
			driverError(c, http.StatusBadRequest, "invalid_owner_contact", "Enter a valid owner email")
			return
		}
		email = e
	}
	if !okPhone && email == "" {
		driverError(c, http.StatusBadRequest, "invalid_owner_contact", "Enter the owner's phone number")
		return
	}
	var recent int
	if err := a.pool.QueryRow(c.Request.Context(), `SELECT count(*) FROM hero_owner_code_requests WHERE hero_id=$1::uuid AND created_at>now()-interval '1 hour'`, heroID(c)).Scan(&recent); err != nil {
		heroFailure(c)
		return
	}
	if recent >= ownerCodeHeroPerHour {
		driverError(c, http.StatusTooManyRequests, "owner_code_limit", "Too many codes requested. Try again in an hour.")
		return
	}
	sendEmail := func(ctx context.Context, to, code string) error {
		subject := "Your heyGo venue confirmation code"
		text := "Your heyGo code is " + code + ". Read it to the heyGo Hero who is listing your venue. It expires in 5 minutes. If you were not expecting this, ignore this email."
		html := "<p>Your heyGo code is <strong>" + code + "</strong>.</p><p>Read it to the heyGo Hero who is listing your venue. It expires in 5 minutes. If you were not expecting this, ignore this email.</p>"
		return sendConfiguredEmail(ctx, a.pool, a.email.settingsKey, to, subject, text, html)
	}
	// Text first; the email is a fallback when texting is unavailable or fails.
	if okPhone && sms != nil {
		id, sent := a.issueOwnerCode(c, "phone", phone, sms.SendOTP)
		if id == "" {
			return
		}
		if sent {
			heroData(c, gin.H{"challengeId": id, "channel": "phone", "sentTo": maskDestination("phone", phone), "expiresIn": int(otpLifetime.Seconds()), "resendAfter": ownerCodeResendSecond})
			return
		}
		if email == "" {
			driverError(c, http.StatusBadGateway, "owner_code_failed", "The code could not be texted. Check the number, or add the owner's email to send it there.")
			return
		}
	}
	if email == "" {
		driverError(c, http.StatusServiceUnavailable, "owner_code_unavailable", "Texting codes is unavailable right now. Add the owner's email, or confirm later.")
		return
	}
	id, sent := a.issueOwnerCode(c, "email", email, sendEmail)
	if id == "" {
		return
	}
	if !sent {
		driverError(c, http.StatusBadGateway, "owner_code_failed", "The code could not be sent. Try again, or confirm later.")
		return
	}
	heroData(c, gin.H{"challengeId": id, "channel": "email", "sentTo": maskDestination("email", email), "expiresIn": int(otpLifetime.Seconds()), "resendAfter": ownerCodeResendSecond})
}

func (a *heroesAPI) verifyOwnerCode(c *gin.Context) {
	var b struct {
		ChallengeID string `json:"challengeId"`
		Code        string `json:"code"`
	}
	invalid := func() {
		driverError(c, http.StatusBadRequest, "invalid_owner_code", "That code is not right or has expired")
	}
	if c.ShouldBindJSON(&b) != nil || len(b.Code) != ownerCodeDigits {
		invalid()
		return
	}
	if _, err := uuid.Parse(b.ChallengeID); err != nil {
		invalid()
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	var channel, destination, hash string
	var attempts int
	var expires time.Time
	var consumed *time.Time
	err = tx.QueryRow(ctx, `SELECT ch.channel,ch.destination,ch.code_hash,ch.attempts,ch.expires_at,ch.consumed_at FROM auth_otp_challenges ch JOIN hero_owner_code_requests r ON r.challenge_id=ch.id WHERE ch.id=$1::uuid AND ch.purpose='venue_owner' AND r.hero_id=$2::uuid FOR UPDATE OF ch`, b.ChallengeID, heroID(c)).Scan(&channel, &destination, &hash, &attempts, &expires, &consumed)
	if err != nil || consumed != nil || time.Now().After(expires) || attempts >= otpMaxAttempts {
		invalid()
		return
	}
	if subtle.ConstantTimeCompare([]byte(otpHash(a.pepper, b.ChallengeID, channel+":"+destination, b.Code)), []byte(hash)) != 1 {
		_, _ = tx.Exec(ctx, `UPDATE auth_otp_challenges SET attempts=attempts+1 WHERE id=$1::uuid`, b.ChallengeID)
		_ = tx.Commit(ctx)
		invalid()
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_otp_challenges SET consumed_at=now() WHERE id=$1::uuid`, b.ChallengeID); err != nil {
		heroFailure(c)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"confirmationId": b.ChallengeID, "channel": channel})
}

// ownerConfirmationValid says whether this Hero holds a fresh, unused confirmation for the
// contact they are listing.
func (a *heroesAPI) ownerConfirmationValid(ctx context.Context, hero, confirmationID, phone, email string) bool {
	if _, err := uuid.Parse(confirmationID); err != nil {
		return false
	}
	var channel, destination string
	err := a.pool.QueryRow(ctx, `SELECT ch.channel,ch.destination FROM auth_otp_challenges ch JOIN hero_owner_code_requests r ON r.challenge_id=ch.id
WHERE ch.id=$1::uuid AND ch.purpose='venue_owner' AND r.hero_id=$2::uuid AND ch.consumed_at IS NOT NULL AND ch.consumed_at>now()-$3::interval
AND NOT EXISTS(SELECT 1 FROM hero_venue_submissions v WHERE v.owner_confirmation_id=ch.id)`, confirmationID, hero, fmt.Sprintf("%d seconds", int(ownerConfirmationFor.Seconds()))).Scan(&channel, &destination)
	if err != nil {
		return false
	}
	if channel == "phone" {
		return phone != "" && destination == phone
	}
	return email != "" && destination == email
}
