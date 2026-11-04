package handler

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/referrals"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func referralError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, referrals.ErrUnavailable):
		driverError(c, 409, "referrals_disabled", err.Error())
	case errors.Is(err, referrals.ErrIneligible), errors.Is(err, referrals.ErrConflict):
		driverError(c, 409, "referral_ineligible", err.Error())
	case errors.Is(err, referrals.ErrInvalid):
		driverError(c, 404, "referral_not_found", "Referral code not found")
	default:
		driverError(c, 503, "referrals_unavailable", "Couldn’t load referrals. Try again.")
	}
}
func registerReferralPublicRoutes(r *gin.Engine, pool *pgxpool.Pool) {
	r.GET("/r/:code", func(c *gin.Context) { referralInvitePage(c, pool) })
	r.GET("/referrals/config", func(c *gin.Context) {
		s, e := referrals.Read(c.Request.Context(), pool)
		if e != nil {
			referralError(c, e)
			return
		}
		c.Header("Cache-Control", "no-store")
		heroData(c, s)
	})
}
func registerReferralRoutes(r *gin.RouterGroup, pool *pgxpool.Pool) {
	for _, role := range []string{"driver", "rider"} {
		role := role
		r.GET("/"+role+"/referrals", func(c *gin.Context) { referralSummary(c, pool, role) })
		r.GET("/"+role+"/referrals/referrer", func(c *gin.Context) { referralReferrer(c, pool) })
		r.GET("/"+role+"/referrals/check", func(c *gin.Context) {
			u, _ := gatewayauth.CurrentUser(c)
			var open bool
			if e := pool.QueryRow(c.Request.Context(), `SELECT referral_signup_open AND cardinality(roles)=0 FROM users WHERE id=$1::uuid`, u.ID).Scan(&open); e != nil {
				referralError(c, e)
				return
			}
			if !open {
				referralError(c, referrals.ErrIneligible)
				return
			}
			p, e := referrals.Lookup(c.Request.Context(), pool, c.Query("code"), role)
			if e != nil {
				referralError(c, e)
				return
			}
			heroData(c, p)
		})
		r.POST("/"+role+"/referrals/claim", func(c *gin.Context) {
			u, _ := gatewayauth.CurrentUser(c)
			var b struct {
				Code string `json:"code"`
			}
			if c.ShouldBindJSON(&b) != nil {
				referralError(c, referrals.ErrInvalid)
				return
			}
			tx, e := pool.Begin(c.Request.Context())
			if e != nil {
				referralError(c, e)
				return
			}
			defer tx.Rollback(c.Request.Context())
			if e = referrals.Claim(c.Request.Context(), tx, u.ID, b.Code, role); e == nil {
				e = tx.Commit(c.Request.Context())
			}
			if e != nil {
				referralError(c, e)
				return
			}
			heroData(c, gin.H{"linked": true})
		})
	}
}
func referralReferrer(c *gin.Context, pool *pgxpool.Pool) {
	u, _ := gatewayauth.CurrentUser(c)
	ctx := c.Request.Context()
	var open bool
	if e := pool.QueryRow(ctx, `SELECT referral_signup_open AND cardinality(roles)=0 FROM users WHERE id=$1::uuid`, u.ID).Scan(&open); e != nil {
		referralError(c, e)
		return
	}
	var name, code string
	e := pool.QueryRow(ctx, `SELECT split_part(COALESCE(h.name,dp.display_name,rp.display_name,'Member'),' ',1),COALESCE(h.referral_code,rc.code) FROM referral_attributions a LEFT JOIN heroes_accounts h ON h.id=a.hero_id LEFT JOIN referral_codes rc ON rc.user_id=a.inviter_id AND rc.inviter_role=a.inviter_role LEFT JOIN driver_profiles dp ON dp.driver_id=a.inviter_id LEFT JOIN rider_profiles rp ON rp.rider_id=a.inviter_id WHERE a.invitee_id=$1::uuid UNION ALL SELECT split_part(h.name,' ',1),h.referral_code FROM heroes_referrals r JOIN heroes_accounts h ON h.id=r.hero_id WHERE r.driver_id=$1::uuid`, u.ID).Scan(&name, &code)
	if errors.Is(e, pgx.ErrNoRows) {
		heroData(c, gin.H{"referrer": nil, "canEnterCode": open})
		return
	}
	if e != nil {
		referralError(c, e)
		return
	}
	heroData(c, gin.H{"referrer": gin.H{"name": name, "code": code}, "canEnterCode": false})
}
func referralSummary(c *gin.Context, pool *pgxpool.Pool, role string) {
	u, _ := gatewayauth.CurrentUser(c)
	if !u.HasRole(role) {
		driverError(c, 403, "role_required", "Account role required")
		return
	}
	ctx := c.Request.Context()
	s, e := referrals.Read(ctx, pool)
	if e != nil {
		referralError(c, e)
		return
	}
	code, e := referrals.OwnCode(ctx, pool, u.ID, role)
	if e != nil {
		referralError(c, e)
		return
	}
	offset, _ := strconv.Atoi(c.Query("cursor"))
	if offset < 0 || offset > 1000000 {
		driverError(c, 400, "invalid_cursor", "Invalid page")
		return
	}
	var earned, pending int64
	var invited, progress, rewarded int
	e = pool.QueryRow(ctx, `SELECT count(*),count(*)FILTER(WHERE credited_at IS NULL AND (qualified_at IS NOT NULL OR deadline_at IS NULL OR deadline_at>=now())),count(*)FILTER(WHERE credited_at IS NOT NULL),COALESCE(sum(reward_kobo)FILTER(WHERE credited_at IS NOT NULL),0),COALESCE(sum(reward_kobo)FILTER(WHERE qualified_at IS NOT NULL AND credited_at IS NULL),0) FROM referral_attributions WHERE inviter_id=$1::uuid AND inviter_role=$2`, u.ID, role).Scan(&invited, &progress, &rewarded, &earned, &pending)
	if e != nil {
		referralError(c, e)
		return
	}
	rows, e := pool.Query(ctx, `SELECT a.id::text,split_part(COALESCE(NULLIF(dp.display_name,''),NULLIF(rp.display_name,''),'Member'),' ',1),right(COALESCE(u.phone_e164,''),4),a.invitee_role,`+referrals.ProgressSQL+`,a.trip_target,a.qualified_at,a.credited_at,a.deadline_at FROM referral_attributions a JOIN users u ON u.id=a.invitee_id LEFT JOIN driver_profiles dp ON dp.driver_id=u.id LEFT JOIN rider_profiles rp ON rp.rider_id=u.id WHERE a.inviter_id=$1::uuid AND a.inviter_role=$2 ORDER BY a.linked_at DESC,a.id DESC LIMIT 51 OFFSET $3`, u.ID, role, offset)
	if e != nil {
		referralError(c, e)
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var id, name, phone, target string
		var trips, goal int
		var qualified, credited, deadline *time.Time
		if e = rows.Scan(&id, &name, &phone, &target, &trips, &goal, &qualified, &credited, &deadline); e != nil {
			referralError(c, e)
			return
		}
		status := "inProgress"
		if credited != nil {
			status = "rewarded"
		} else if qualified != nil {
			status = "pendingApproval"
		} else if deadline != nil && deadline.Before(time.Now()) {
			status = "notQualified"
		}
		items = append(items, gin.H{"id": id, "firstName": name, "phoneLast4": phone, "audience": target, "status": status, "progress": fmt.Sprintf("%d / %d trips", trips, goal)})
	}
	if e = rows.Err(); e != nil {
		referralError(c, e)
		return
	}
	var next *string
	if len(items) > 50 {
		v := strconv.Itoa(offset + 50)
		next = &v
		items = items[:50]
	}
	headline, body := s.DriverHeadline, s.DriverBody
	if role == "rider" {
		headline, body = s.RiderHeadline, s.RiderBody
	}
	base := strings.TrimRight(env.GetString("REFERRAL_PUBLIC_URL", ""), "/")
	if base == "" {
		base = "https://" + c.Request.Host
	}
	heroData(c, gin.H{"enabled": s.Enabled, "disabledNotice": s.DisabledNotice, "headline": headline, "body": body, "code": code, "link": base + "/r/" + code, "earnedKobo": earned, "pendingKobo": pending, "invited": invited, "inProgress": progress, "rewarded": rewarded, "invitations": items, "nextCursor": next, "rules": s.Rules})
}
func (a *adminAPI) getReferralSettings(c *gin.Context) {
	s, e := referrals.Read(c.Request.Context(), a.pool)
	if e != nil {
		referralError(c, e)
		return
	}
	heroData(c, s)
}
func (a *adminAPI) saveReferralSettings(c *gin.Context) {
	var s referrals.Settings
	if c.ShouldBindJSON(&s) != nil || s.Validate() != nil {
		driverError(c, 400, "invalid_referral_settings", "Check reward rules and banner text.")
		return
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		referralError(c, e)
		return
	}
	defer tx.Rollback(ctx)
	s, e = referrals.Save(ctx, tx, s, currentAdmin(c).ID)
	if errors.Is(e, referrals.ErrStale) {
		driverError(c, 409, "stale_referral_settings", e.Error())
		return
	}
	if e == nil {
		e = adminAudit(c, tx, "referrals.settings.changed", "", gin.H{"version": s.Version, "enabled": s.Enabled})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		referralError(c, e)
		return
	}
	heroData(c, s)
}
func (a *adminAPI) referralRewards(c *gin.Context) {
	offset, _ := strconv.Atoi(c.Query("offset"))
	if offset < 0 || offset > 1000000 {
		driverError(c, 400, "invalid_page", "Invalid page")
		return
	}
	status := c.DefaultQuery("status", "pending")
	if status != "pending" && status != "credited" && status != "progress" && status != "all" {
		driverError(c, 400, "invalid_status", "Invalid status")
		return
	}
	rows, e := a.pool.Query(c.Request.Context(), `SELECT a.id::text,COALESCE(NULLIF(dp.display_name,''),NULLIF(rp.display_name,''),h.name,'Member'),COALESCE(NULLIF(idp.display_name,''),NULLIF(irp.display_name,''),'Member'),a.inviter_role,a.invitee_role,a.reward_kobo,a.trip_target,`+referrals.ProgressSQL+`,a.linked_at,a.qualified_at,a.credited_at,a.settings_version,a.deadline_at,COALESCE(a.inviter_id::text,a.hero_id::text) FROM referral_attributions a LEFT JOIN heroes_accounts h ON h.id=a.hero_id LEFT JOIN driver_profiles dp ON dp.driver_id=a.inviter_id LEFT JOIN rider_profiles rp ON rp.rider_id=a.inviter_id LEFT JOIN driver_profiles idp ON idp.driver_id=a.invitee_id LEFT JOIN rider_profiles irp ON irp.rider_id=a.invitee_id WHERE ($1='all' OR ($1='pending' AND a.qualified_at IS NOT NULL AND a.credited_at IS NULL) OR ($1='credited' AND a.credited_at IS NOT NULL) OR ($1='progress' AND a.qualified_at IS NULL)) ORDER BY a.linked_at DESC,a.id DESC LIMIT 51 OFFSET $2`, status, offset)
	if e != nil {
		referralError(c, e)
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var id, name, invitee, from, to, owner string
		var reward, version int64
		var goal, progress int
		var linked time.Time
		var qualified, credited, deadline *time.Time
		if e = rows.Scan(&id, &name, &invitee, &from, &to, &reward, &goal, &progress, &linked, &qualified, &credited, &version, &deadline, &owner); e != nil {
			referralError(c, e)
			return
		}
		items = append(items, gin.H{"id": id, "referrer": name, "referrerId": owner, "invitee": invitee, "inviterRole": from, "inviteeRole": to, "rewardKobo": reward, "tripTarget": goal, "completedTrips": progress, "linkedAt": linked, "qualifiedAt": qualified, "creditedAt": credited, "version": version, "deadlineAt": deadline})
	}
	if e = rows.Err(); e != nil {
		referralError(c, e)
		return
	}
	more := len(items) > 50
	if more {
		items = items[:50]
	}
	var unread int
	if e = a.pool.QueryRow(c.Request.Context(), `SELECT count(*) FROM referral_admin_notifications n JOIN referral_attributions a ON a.id=n.referral_id WHERE a.credited_at IS NULL`).Scan(&unread); e != nil {
		referralError(c, e)
		return
	}
	heroData(c, gin.H{"items": items, "hasMore": more, "pendingCount": unread})
}
func (a *adminAPI) approveReferralRewards(c *gin.Context) {
	var b struct {
		IDs []string `json:"ids"`
	}
	if c.ShouldBindJSON(&b) != nil || len(b.IDs) < 1 || len(b.IDs) > 100 {
		driverError(c, 400, "invalid_rewards", "Select up to 100 rewards.")
		return
	}
	for _, id := range b.IDs {
		if _, e := uuid.Parse(id); e != nil {
			driverError(c, 400, "invalid_reward", "Invalid reward.")
			return
		}
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		referralError(c, e)
		return
	}
	defer tx.Rollback(ctx)
	if e = referrals.Approve(ctx, tx, b.IDs, currentAdmin(c).ID); e != nil {
		driverError(c, 409, "reward_not_approvable", "A selected reward cannot be approved. Reload and check its status.")
		return
	}
	if e = adminAudit(c, tx, "referrals.rewards.approved", "", gin.H{"ids": b.IDs}); e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		referralError(c, e)
		return
	}
	heroData(c, gin.H{"approved": len(b.IDs)})
}

var referralInviteTemplate = template.Must(template.New("referral").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>heyGo invitation</title><style>body{font:16px system-ui;margin:0;background:#f8faf4;color:#0f172a}main{max-width:440px;margin:8vh auto;padding:24px}h1{font-size:30px}code{display:block;font-size:28px;font-weight:700;margin:24px 0;overflow-wrap:anywhere}button{background:#168a43;color:white;border:0;border-radius:12px;padding:16px 24px;font:inherit;cursor:pointer}a{color:#168a43}p{line-height:1.6}</style></head><body><main><strong>heyGo</strong><h1>{{.Title}}</h1><p>{{.Notice}}</p>{{if .Code}}<code id="code">{{.Code}}</code><button id="copy">Copy code</button><p id="feedback" role="status"></p><p>Enter this code during signup. You can confirm one referrer. Existing accounts cannot add a code.</p><p>Reward conditions are shown in the app.</p><a href="https://heygo.ng/">About riding</a>{{if .Drivers}} · <a href="https://heygo.ng/drive/">About driving</a>{{end}}<script>document.getElementById('copy').onclick=async function(){try{await navigator.clipboard.writeText(document.getElementById('code').textContent);document.getElementById('feedback').textContent='Code copied';}catch(e){document.getElementById('feedback').textContent='Select and copy the code above.';}}</script>{{end}}</main></body></html>`))

func referralInvitePage(c *gin.Context, pool *pgxpool.Pool) {
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "no-referrer")
	r, err := referrals.Lookup(c.Request.Context(), pool, c.Param("code"), "rider")
	if errors.Is(err, referrals.ErrInvalid) {
		r, err = referrals.Lookup(c.Request.Context(), pool, c.Param("code"), "driver")
	}
	data := gin.H{"Title": "You’re invited to heyGo", "Notice": "Open heyGo to ride or drive."}
	status := http.StatusOK
	if err != nil {
		status = http.StatusNotFound
		data["Title"] = "Invitation unavailable"
		data["Notice"] = "This invitation is unavailable."
		if errors.Is(err, referrals.ErrUnavailable) {
			status = http.StatusConflict
			s, e := referrals.Read(c.Request.Context(), pool)
			if e == nil {
				data["Notice"] = s.DisabledNotice
			}
		}
	} else {
		data["Code"] = r.Code
		data["Drivers"] = r.Role != "rider"
		if r.Role == "rider" {
			data["Notice"] = "You’re invited to ride with heyGo."
		} else if r.Role == "hero" {
			data["Notice"] = "You’re invited to drive with heyGo."
		}
	}
	c.Status(status)
	if err = referralInviteTemplate.Execute(c.Writer, data); err != nil {
		c.Error(err)
	}
}
