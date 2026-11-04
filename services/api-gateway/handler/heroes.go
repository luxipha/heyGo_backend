package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/httpmiddleware"
	"github.com/luxipha/heyGo_backend/shared/referrals"
)

type heroesAPI struct {
	pool   *pgxpool.Pool
	email  *adminAPI
	pepper []byte
}

func heroesToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func heroesHash(s string) string     { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func heroID(c *gin.Context) string   { return c.GetString("hero-id") }
func heroData(c *gin.Context, v any) { c.JSON(200, gin.H{"data": v}) }
func heroFailure(c *gin.Context) {
	driverError(c, 503, "heroes_unavailable", "Heroes is temporarily unavailable")
}
func heroEmail(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	a, e := mail.ParseAddress(s)
	return s, e == nil && a.Address == s && len(s) <= 254
}

func registerHeroesRoutes(r *gin.Engine, pool *pgxpool.Pool) *heroesAPI {
	if pool == nil {
		return nil
	}
	key, _ := hex.DecodeString(env.GetString("HEYGO_ADMIN_SETTINGS_KEY", ""))
	a := &heroesAPI{pool: pool, email: &adminAPI{pool: pool, settingsKey: key}, pepper: []byte(env.GetString("OTP_PEPPER", ""))}
	g := r.Group("/heroes")
	g.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16384)
		c.Next()
	})
	g.POST("/auth/otp/request", httpmiddleware.NewRateLimiter(10, time.Minute).Middleware, a.requestEmailOTP)
	g.POST("/auth/otp/verify", httpmiddleware.NewRateLimiter(30, time.Minute).Middleware, a.verifyEmailOTP)
	g.GET("/referrals/:code", a.code)
	g.Use(a.authenticate)
	g.POST("/auth/logout", a.logout)
	g.GET("/me", a.me)
	g.POST("/profile", a.saveProfile)
	g.GET("/drivers", a.drivers)
	g.POST("/drivers", a.addDriver)
	g.GET("/wallet", a.wallet)
	g.POST("/bank-account", a.saveBank)
	g.GET("/leaderboard", a.leaderboard)
	return a
}
func (a *heroesAPI) authenticate(c *gin.Context) {
	raw := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	var id string
	err := a.pool.QueryRow(c.Request.Context(), `SELECT h.id::text FROM heroes_sessions s JOIN heroes_accounts h ON h.id=s.hero_id WHERE s.token_hash=$1 AND s.expires_at>now()AND h.active AND (s.review_otp_version IS NULL OR EXISTS(SELECT 1 FROM admin_review_otp r WHERE r.id=1 AND r.enabled AND r.expires_at>now() AND r.version=s.review_otp_version AND r.email=h.email AND octet_length(r.code_ciphertext)>0))`, heroesHash(raw)).Scan(&id)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			heroFailure(c)
		} else {
			driverError(c, 401, "hero_authentication_required", "Sign in to Heroes")
		}
		c.Abort()
		return
	}
	c.Set("hero-id", id)
	c.Next()
}
func (a *heroesAPI) session(c *gin.Context, id string) {
	token, err := heroesToken()
	if err == nil {
		_, err = a.pool.Exec(c.Request.Context(), `INSERT INTO heroes_sessions(token_hash,hero_id,expires_at)VALUES($1,$2::uuid,now()+interval '7 days')`, heroesHash(token), id)
	}
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"token": token, "expiresIn": 604800})
}
func (a *heroesAPI) logout(c *gin.Context) {
	_, err := a.pool.Exec(c.Request.Context(), `DELETE FROM heroes_sessions WHERE token_hash=$1 AND hero_id=$2::uuid`, heroesHash(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")), heroID(c))
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"signedOut": true})
}
func (a *heroesAPI) code(c *gin.Context) {
	r, err := referrals.Lookup(c.Request.Context(), a.pool, c.Param("code"), "driver")
	if err != nil {
		referralError(c, err)
		return
	}
	heroData(c, r)
}

func (a *heroesAPI) me(c *gin.Context) {
	var id, name, email, code string
	var total, onboarded int
	var needsName bool
	var ranks json.RawMessage
	err := a.pool.QueryRow(c.Request.Context(), `SELECT h.id::text,h.name,h.email,h.referral_code,h.profile_completed_at IS NULL,(SELECT count(*)FROM heroes_all_referrals r WHERE r.hero_id=h.id),(SELECT count(*)FROM heroes_all_referrals r WHERE r.hero_id=h.id AND r.onboarded_at IS NOT NULL),(SELECT ranks FROM heroes_policies ORDER BY version DESC LIMIT 1)FROM heroes_accounts h WHERE h.id=$1::uuid`, heroID(c)).Scan(&id, &name, &email, &code, &needsName, &total, &onboarded, &ranks)
	if err != nil {
		heroFailure(c)
		return
	}
	var thresholds map[string]int
	if json.Unmarshal(ranks, &thresholds) != nil {
		heroFailure(c)
		return
	}
	rank := "E"
	var next any
	for _, r := range []string{"E", "D", "C", "B", "A"} {
		if threshold, ok := thresholds[r]; ok {
			if onboarded >= threshold {
				rank = r
			} else if next == nil {
				next = gin.H{"rank": r, "target": threshold, "remaining": threshold - onboarded}
			}
		}
	}
	heroData(c, gin.H{"id": id, "name": name, "needsName": needsName, "email": email, "code": code, "referralLink": strings.TrimRight(env.GetString("HEROES_PUBLIC_URL", "https://hero.heygo.ng"), "/") + "/r/" + code, "referred": total, "onboarded": onboarded, "rank": rank, "nextRank": next})
}
func (a *heroesAPI) addDriver(c *gin.Context) {
	var b struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_driver_lead", "Enter the driver's name and phone")
		return
	}
	phone, valid := normalizePhone(b.Phone)
	name := strings.TrimSpace(b.Name)
	if !valid || len(name) < 1 || len(name) > 100 {
		driverError(c, 400, "invalid_driver_lead", "Enter a name and valid Nigerian phone number")
		return
	}
	_, err := a.pool.Exec(c.Request.Context(), `INSERT INTO heroes_driver_leads(id,hero_id,name,phone)VALUES($1::uuid,$2::uuid,$3,$4)ON CONFLICT(hero_id,phone)DO NOTHING`, uuid.NewString(), heroID(c), name, phone)
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"saved": true})
}
func (a *heroesAPI) drivers(c *gin.Context) {
	rows, err := a.pool.Query(c.Request.Context(), `SELECT r.id::text,COALESCE(NULLIF(l.name,''),NULLIF(dp.display_name,''),'Driver'),u.phone_e164,r.claimed_at,p.trip_target,p.reward_kobo,p.enabled,r.qualified_at IS NOT NULL,COALESCE(e.eligible_to_drive,false),(SELECT count(*)FROM trips t WHERE t.assigned_driver_id=r.driver_id AND t.status='completed' AND t.created_at>=r.claimed_at AND t.rider_id<>r.driver_id AND NOT EXISTS(SELECT 1 FROM simulation_participants s WHERE s.actor_user_id=t.rider_id)) FROM heroes_all_referrals r JOIN users u ON u.id=r.driver_id JOIN heroes_all_policies p ON p.id=r.policy_id LEFT JOIN heroes_driver_leads l ON l.hero_id=r.hero_id AND l.phone=u.phone_e164 LEFT JOIN driver_profiles dp ON dp.driver_id=u.id LEFT JOIN driver_eligibility e ON e.driver_id=u.id WHERE r.hero_id=$1::uuid ORDER BY r.claimed_at DESC LIMIT 200`, heroID(c))
	if err != nil {
		heroFailure(c)
		return
	}
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, name, phone string
		var joined time.Time
		var target, trips int
		var reward int64
		var enabled, credited, onboarded bool
		if err = rows.Scan(&id, &name, &phone, &joined, &target, &reward, &enabled, &credited, &onboarded, &trips); err != nil {
			rows.Close()
			heroFailure(c)
			return
		}
		items = append(items, gin.H{"id": id, "name": name, "phone": maskHeroPhone(phone), "joinedAt": joined, "tripTarget": target, "trips": trips, "rewardKobo": reward, "rewardsEnabled": enabled, "credited": credited, "onboarded": onboarded, "linked": true})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		heroFailure(c)
		return
	}
	leads, err := a.pool.Query(c.Request.Context(), `SELECT l.id::text,l.name,l.phone,l.created_at FROM heroes_driver_leads l WHERE l.hero_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM heroes_all_referrals r JOIN users u ON u.id=r.driver_id WHERE r.hero_id=l.hero_id AND u.phone_e164=l.phone)ORDER BY l.created_at DESC LIMIT 200`, heroID(c))
	if err != nil {
		heroFailure(c)
		return
	}
	defer leads.Close()
	for leads.Next() {
		var id, name, phone string
		var joined time.Time
		if leads.Scan(&id, &name, &phone, &joined) != nil {
			heroFailure(c)
			return
		}
		items = append(items, gin.H{"id": id, "name": name, "phone": maskHeroPhone(phone), "joinedAt": joined, "linked": false})
	}
	if leads.Err() != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"items": items, "limit": 200})
}
func maskHeroPhone(phone string) string {
	if len(phone) < 5 {
		return "••••"
	}
	return "•••• " + phone[len(phone)-4:]
}
func (a *heroesAPI) wallet(c *gin.Context) {
	var balance, pending, paid int64
	err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT sum(amount_kobo)FROM heroes_wallet_entries WHERE hero_id=$1::uuid),0),COALESCE((SELECT sum(p.reward_kobo)FROM heroes_all_referrals r JOIN heroes_all_policies p ON p.id=r.policy_id WHERE r.hero_id=$1::uuid AND r.qualified_at IS NULL AND p.enabled),0),COALESCE((SELECT sum(p.amount_kobo)FROM heroes_manual_payments p WHERE p.hero_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM heroes_payment_reversals v WHERE v.payment_id=p.id)),0)`, heroID(c)).Scan(&balance, &pending, &paid)
	if err != nil {
		heroFailure(c)
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `SELECT id::text,amount_kobo,created_at FROM heroes_wallet_entries WHERE hero_id=$1::uuid ORDER BY created_at DESC,id DESC LIMIT 100`, heroID(c))
	if err != nil {
		heroFailure(c)
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id string
		var amount int64
		var at time.Time
		if rows.Scan(&id, &amount, &at) != nil {
			heroFailure(c)
			return
		}
		items = append(items, gin.H{"id": id, "amountKobo": amount, "createdAt": at, "kind": "referral_reward"})
	}
	if rows.Err() != nil {
		heroFailure(c)
		return
	}
	bank, err := a.readBank(c, heroID(c), false)
	if err != nil {
		heroFailure(c)
		return
	}
	payments, err := a.payments(c, heroID(c))
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"balanceKobo": balance - paid, "earnedKobo": balance, "paidKobo": paid, "bankAccount": bank, "payments": payments, "pendingKobo": pending, "currency": "NGN", "entries": items})
}
func (a *heroesAPI) leaderboard(c *gin.Context) {
	rows, err := a.pool.Query(c.Request.Context(), `SELECT h.id::text,split_part(h.name,' ',1),count(r.id)FROM heroes_accounts h JOIN heroes_referrals r ON r.hero_id=h.id WHERE r.onboarded_at IS NOT NULL AND h.active GROUP BY h.id ORDER BY count(r.id)DESC,h.created_at,h.id LIMIT 50`)
	if err != nil {
		heroFailure(c)
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, name string
		var count int
		if rows.Scan(&id, &name, &count) != nil {
			heroFailure(c)
			return
		}
		items = append(items, gin.H{"name": name, "onboarded": count, "position": len(items) + 1, "isYou": id == heroID(c)})
	}
	if rows.Err() != nil {
		heroFailure(c)
		return
	}
	heroData(c, items)
}
func claimHeroReferral(c *gin.Context, pool *pgxpool.Pool) {
	u, ok := gatewayauth.CurrentUser(c)
	if !ok {
		driverError(c, 401, "authentication_required", "Sign in as a Driver")
		return
	}
	var b struct {
		Code string `json:"code"`
	}
	if c.ShouldBindJSON(&b) != nil || len(b.Code) > 32 {
		driverError(c, 400, "invalid_referral", "Enter a valid referral code")
		return
	}
	tx, err := pool.Begin(c.Request.Context())
	if err != nil {
		referralError(c, err)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if err = referrals.Claim(c.Request.Context(), tx, u.ID, b.Code, "driver"); err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		referralError(c, err)
		return
	}

	heroData(c, gin.H{"linked": true})
}

func getDriverHeroReferral(c *gin.Context, pool *pgxpool.Pool) {
	u, ok := gatewayauth.CurrentUser(c)
	if !ok {
		driverError(c, 401, "authentication_required", "Sign in as a Driver")
		return
	}
	var name, code string
	err := pool.QueryRow(c.Request.Context(), `SELECT h.name,h.referral_code FROM heroes_all_referrals r JOIN heroes_accounts h ON h.id=r.hero_id WHERE r.driver_id=$1::uuid`, u.ID).Scan(&name, &code)
	if errors.Is(err, pgx.ErrNoRows) {
		heroData(c, gin.H{"hero": nil})
		return
	}
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"hero": gin.H{"name": name, "code": code}})
}
