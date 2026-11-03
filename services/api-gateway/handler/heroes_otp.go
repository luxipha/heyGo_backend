package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (a *heroesAPI) otpHash(id, code string) string {
	mac := hmac.New(sha256.New, a.pepper)
	mac.Write([]byte("heroes-email-otp:" + id + ":" + code))
	return hex.EncodeToString(mac.Sum(nil))
}
func (a *heroesAPI) requestEmailOTP(c *gin.Context) {
	var b struct {
		Email string `json:"email"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_email", "Enter your email")
		return
	}
	email, valid := heroEmail(b.Email)
	if !valid {
		driverError(c, 400, "invalid_email", "Enter a valid email")
		return
	}
	if len(a.pepper) < 32 {
		heroFailure(c)
		return
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		heroFailure(c)
		return
	}
	code := fmt.Sprintf("%06d", n.Int64())
	id := uuid.NewString()
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "heroes-otp:"+email); err != nil {
		heroFailure(c)
		return
	}
	var recent bool
	if err = tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM heroes_email_otps WHERE email=$1 AND requested_at>now()-interval '60 seconds')`, email).Scan(&recent); err != nil {
		heroFailure(c)
		return
	}
	if recent {
		driverError(c, 429, "otp_cooldown", "Please wait a minute before requesting another code")
		return
	}

	expires := time.Now().UTC().Add(5 * time.Minute)
	var reviewVersion *int64
	review, err := readReviewOTP(c.Request.Context(), tx)
	if err != nil {
		heroFailure(c)
		return
	}
	if review.active() && review.Email == email {
		if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(680001)`); err != nil {
			heroFailure(c)
			return
		}
		review, err = readReviewOTP(c.Request.Context(), tx)
		if err != nil {
			heroFailure(c)
			return
		}
		if review.active() && review.Email == email {
			code, err = openAdminValue(a.email.settingsKey, review.Ciphertext, "heygo-review-otp-v1")
			if err != nil {
				heroFailure(c)
				return
			}
			reviewVersion = &review.Version
			if review.ExpiresAt.Before(expires) {
				expires = review.ExpiresAt
			}
		}
	}
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO heroes_email_otps(email,challenge_id,code_hash,expires_at,review_otp_version)VALUES($1,$2::uuid,$3,$4,$5) ON CONFLICT(email)DO UPDATE SET challenge_id=EXCLUDED.challenge_id,code_hash=EXCLUDED.code_hash,expires_at=EXCLUDED.expires_at,review_otp_version=EXCLUDED.review_otp_version,requested_at=now(),attempts=0,used_at=NULL`, email, id, a.otpHash(id, code), expires, reviewVersion)
	if err != nil {
		heroFailure(c)
		return
	}
	if reviewVersion == nil {
		if err = a.email.sendStaffEmail(c.Request.Context(), email, "Your heyGo Heroes code", "Your heyGo Heroes code is "+code+". It expires in 5 minutes. If you did not request this code, ignore this email."); err != nil {
			driverError(c, 503, "email_unavailable", "Could not send your code. Please try again.")
			return
		}
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		heroFailure(c)
		return
	}
	delivery := "provider"
	if reviewVersion != nil {
		delivery = "review"
	}
	heroData(c, gin.H{"challengeId": id, "expiresIn": int(time.Until(expires).Seconds()), "resendAfter": 60, "delivery": delivery})
}
func (a *heroesAPI) verifyEmailOTP(c *gin.Context) {
	var b struct {
		ChallengeID string `json:"challengeId"`
		Code        string `json:"code"`
	}
	invalid := func() { driverError(c, 400, "invalid_otp", "This code is incorrect or expired") }
	if c.ShouldBindJSON(&b) != nil || len(b.Code) != 6 || len(a.pepper) < 32 {
		invalid()
		return
	}
	if _, err := uuid.Parse(b.ChallengeID); err != nil {
		invalid()
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var email, hash string
	var expires time.Time
	var attempts int
	var used *time.Time
	var reviewVersion *int64
	err = tx.QueryRow(c.Request.Context(), `SELECT email,code_hash,expires_at,attempts,used_at,review_otp_version FROM heroes_email_otps WHERE challenge_id=$1::uuid FOR UPDATE`, b.ChallengeID).Scan(&email, &hash, &expires, &attempts, &used, &reviewVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		invalid()
		return
	}
	if err != nil {
		heroFailure(c)
		return
	}
	if used != nil || attempts >= 5 || !time.Now().Before(expires) {
		invalid()
		return
	}

	sessionExpiry := time.Now().UTC().Add(7 * 24 * time.Hour)
	if reviewVersion != nil {
		if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(680001)`); err != nil {
			heroFailure(c)
			return
		}
		review, e := readReviewOTP(c.Request.Context(), tx)
		if e != nil {
			heroFailure(c)
			return
		}
		if !review.active() || review.Version != *reviewVersion || review.Email != email {
			invalid()
			return
		}
		if review.ExpiresAt.Before(sessionExpiry) {
			sessionExpiry = review.ExpiresAt
		}
	}
	if !hmac.Equal([]byte(hash), []byte(a.otpHash(b.ChallengeID, b.Code))) {
		if _, err = tx.Exec(c.Request.Context(), `UPDATE heroes_email_otps SET attempts=attempts+1 WHERE challenge_id=$1::uuid`, b.ChallengeID); err != nil {
			heroFailure(c)
			return
		}
		if err = tx.Commit(c.Request.Context()); err != nil {
			heroFailure(c)
			return
		}
		invalid()
		return
	}
	var id string
	var active bool
	err = tx.QueryRow(c.Request.Context(), `SELECT id::text,active FROM heroes_accounts WHERE email=$1`, email).Scan(&id, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		tag, e := heroesToken()
		if e != nil {
			heroFailure(c)
			return
		}
		id = uuid.NewString()
		active = true
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO heroes_accounts(id,email,name,referral_code)VALUES($1::uuid,$2,'',$3)`, id, email, "H"+strings.ToUpper(tag[:9]))
	}
	if err != nil {
		heroFailure(c)
		return
	}
	if !active {
		driverError(c, 403, "hero_inactive", "This account is unavailable")
		return
	}
	token, err := heroesToken()
	if err != nil {
		heroFailure(c)
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `UPDATE heroes_email_otps SET used_at=now() WHERE challenge_id=$1::uuid`, b.ChallengeID); err == nil {
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO heroes_sessions(token_hash,hero_id,expires_at,review_otp_version)VALUES($1,$2::uuid,$3,$4)`, heroesHash(token), id, sessionExpiry, reviewVersion)
	}
	if err != nil {
		heroFailure(c)
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"token": token, "expiresIn": int(time.Until(sessionExpiry).Seconds())})
}
func (a *heroesAPI) saveProfile(c *gin.Context) {
	var b struct {
		Name string `json:"name"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_name", "Enter your name")
		return
	}
	name := strings.TrimSpace(b.Name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		driverError(c, 400, "invalid_name", "Enter a name of up to 100 characters")
		return
	}
	_, err := a.pool.Exec(c.Request.Context(), `UPDATE heroes_accounts SET name=$2,profile_completed_at=COALESCE(profile_completed_at,now()) WHERE id=$1::uuid`, heroID(c), name)
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"saved": true})
}
