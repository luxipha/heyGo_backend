package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/ideas"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"regexp"
	"strings"
)

var newIdeasClient = ideas.Configured

var featureKey = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

type featureInterestAPI struct{ pool *pgxpool.Pool }

func registerFeatureInterest(g *gin.RouterGroup, p *pgxpool.Pool) {
	a := &featureInterestAPI{p}
	r := g.Group("/rider/features", gatewayauth.RequireRole("rider"))
	r.GET("/:key", a.read)
	r.POST("/:key/notify", a.notify)
	r.POST("/:key/interest", a.vote)
	r.DELETE("/:key/notify", a.unsubscribe)
}
func registerAdminFeatureInterest(g *gin.RouterGroup, a *adminAPI) {
	g.GET("/promotions/features", a.featureList)
	g.POST("/promotions/features", a.requireCSRF, a.featureCreate)
	g.POST("/promotions/features/:key/publish", a.requireCSRF, a.featurePublish)
	g.POST("/promotions/features/:key/disable", a.requireCSRF, a.featureDisable)
	g.POST("/promotions/features/:key/launch", a.requireCSRF, a.featureLaunch)
}
func (a *featureInterestAPI) read(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	u, _ := gatewayauth.CurrentUser(c)
	var raw json.RawMessage
	e := a.pool.QueryRow(c.Request.Context(), `SELECT jsonb_build_object('key',f.key,'title',f.title,'description',f.description,'tagline',f.tagline,'bullets',f.bullets,'question',f.question,'interestTags',f.interest_tags,'ideaId',f.idea_id,'live',f.status='live','voted',EXISTS(SELECT 1 FROM rider_feature_subscriptions s WHERE s.feature_key=f.key AND s.rider_id=$2::uuid AND s.status='confirmed'),'selectedTag',(SELECT s.interest_tag FROM rider_feature_subscriptions s WHERE s.feature_key=f.key AND s.rider_id=$2::uuid),'subscribed',EXISTS(SELECT 1 FROM rider_feature_subscriptions s WHERE s.feature_key=f.key AND s.rider_id=$2::uuid AND s.status='confirmed' AND s.subscribed)) FROM rider_feature_ideas f WHERE key=$1 AND status IN ('published','live')`, c.Param("key"), u.ID).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		driverError(c, 404, "feature_unavailable", "This feature is not accepting interest yet.")
		return
	}
	if e != nil {
		featureFailure(c)
		return
	}
	heroData(c, raw)
}
func (a *featureInterestAPI) notify(c *gin.Context) { a.record(c, true) }
func (a *featureInterestAPI) vote(c *gin.Context)   { a.record(c, false) }
func (a *featureInterestAPI) record(c *gin.Context, subscribe bool) {
	c.Header("Cache-Control", "private, no-store")
	u, _ := gatewayauth.CurrentUser(c)
	var b struct {
		InterestTag string `json:"interestTag"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_interest", "Choose a valid interest option.")
		return
	}
	client, e := newIdeasClient()
	if e != nil {
		driverError(c, 503, "ideas_unavailable", "Notifications are unavailable right now. Please retry.")
		return
	}
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		featureFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var id int64
	var tags []string
	e = tx.QueryRow(c.Request.Context(), `SELECT idea_id,interest_tags FROM rider_feature_ideas WHERE key=$1 AND status='published' FOR SHARE`, c.Param("key")).Scan(&id, &tags)
	if e != nil {
		driverError(c, 404, "feature_unavailable", "This feature is not accepting interest yet.")
		return
	}
	valid := b.InterestTag == ""
	for _, t := range tags {
		valid = valid || t == b.InterestTag
	}
	if !valid {
		driverError(c, 400, "invalid_interest", "Choose a valid interest option.")
		return
	}
	var email string
	e = tx.QueryRow(c.Request.Context(), `SELECT COALESCE(email,'') FROM users WHERE id=$1::uuid AND active AND 'rider'=ANY(roles) AND email_verified`, u.ID).Scan(&email)
	if e != nil || email == "" {
		driverError(c, 409, "verified_email_required", "Verify an email on your HeyGo account to join this list.")
		return
	}
	_, e = tx.Exec(c.Request.Context(), `INSERT INTO rider_feature_subscriptions(feature_key,rider_id,interest_tag) VALUES($1,$2::uuid,$3) ON CONFLICT DO NOTHING`, c.Param("key"), u.ID, b.InterestTag)
	if e != nil {
		featureFailure(c)
		return
	}
	var state string
	if tx.QueryRow(c.Request.Context(), `SELECT status FROM rider_feature_subscriptions WHERE feature_key=$1 AND rider_id=$2::uuid FOR UPDATE`, c.Param("key"), u.ID).Scan(&state) != nil {
		featureFailure(c)
		return
	}
	if state != "confirmed" {
		var reply struct {
			Voted bool `json:"voted"`
		}
		e = client.Post(c.Request.Context(), fmt.Sprintf("/api/v1/heygo/ideas/%d/notify", id), gin.H{"riderId": u.ID, "email": email}, &reply)
		if e != nil || !reply.Voted {
			driverError(c, 503, "ideas_unavailable", "We couldn't save this yet. Please retry.")
			return
		}
		_, e = tx.Exec(c.Request.Context(), `UPDATE rider_feature_subscriptions SET status='confirmed',confirmed_at=NOW() WHERE feature_key=$1 AND rider_id=$2::uuid`, c.Param("key"), u.ID)
	}
	var subscribed bool
	if e == nil {
		e = tx.QueryRow(c.Request.Context(), `UPDATE rider_feature_subscriptions SET subscribed=CASE WHEN $3 THEN TRUE ELSE subscribed END,interest_tag=CASE WHEN $4<>'' THEN $4 ELSE interest_tag END WHERE feature_key=$1 AND rider_id=$2::uuid RETURNING subscribed`, c.Param("key"), u.ID, subscribe, b.InterestTag).Scan(&subscribed)
	}
	if e == nil {
		e = tx.Commit(c.Request.Context())
	}
	if e != nil {
		featureFailure(c)
		return
	}
	heroData(c, gin.H{"voted": true, "subscribed": subscribed})
}
func (a *adminAPI) featureList(c *gin.Context) {
	var rows json.RawMessage
	e := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE(jsonb_agg(v ORDER BY v->>'key'),'[]'::jsonb) FROM(SELECT jsonb_build_object('key',f.key,'title',f.title,'description',f.description,'tagline',f.tagline,'bullets',f.bullets,'question',f.question,'interestTags',f.interest_tags,'status',f.status,'ideaId',f.idea_id,'interestedRiders',(SELECT count(*) FROM rider_feature_subscriptions s WHERE s.feature_key=f.key AND s.status='confirmed'),'subscribers',(SELECT count(*) FROM rider_feature_subscriptions s WHERE s.feature_key=f.key AND s.status='confirmed' AND s.subscribed),'interestCounts',COALESCE((SELECT jsonb_object_agg(tag,n) FROM(SELECT interest_tag tag,count(*) n FROM rider_feature_subscriptions s WHERE s.feature_key=f.key AND s.status='confirmed' AND interest_tag<>'' GROUP BY interest_tag)x),'{}'::jsonb))v FROM rider_feature_ideas f)t`).Scan(&rows)
	if e != nil {
		featureFailure(c)
		return
	}
	_, e = newIdeasClient()
	heroData(c, gin.H{"items": rows, "enabled": e == nil})
}
func (a *adminAPI) featureCreate(c *gin.Context) {
	var b struct {
		Key, Title, Description, Reason, Tagline, Question string
		Bullets                                            []string `json:"bullets"`
		InterestTags                                       []string `json:"interestTags"`
	}
	if c.ShouldBindJSON(&b) != nil || !featureKey.MatchString(b.Key) || len(strings.TrimSpace(b.Title)) < 2 || len(b.Title) > 120 || len(strings.TrimSpace(b.Description)) < 2 || len(b.Description) > 4000 || len(b.InterestTags) > 12 || len(strings.TrimSpace(b.Reason)) < 5 {
		driverError(c, 400, "invalid_feature", "Set a feature key, title, description and audit reason.")
		return
	}
	for _, t := range b.InterestTags {
		if strings.TrimSpace(t) == "" || len(t) > 80 {
			driverError(c, 400, "invalid_tags", "Use short nonempty interest options.")
			return
		}
	}
	if len(b.Tagline) > 240 || len(b.Question) > 240 || len(b.Bullets) > 6 || (len(b.InterestTags) > 0 && len(strings.TrimSpace(b.Question)) < 3) {
		driverError(c, 400, "invalid_feature_content", "Set a feature-specific question for the interest options.")
		return
	}
	for _, line := range b.Bullets {
		if len(strings.TrimSpace(line)) == 0 || len(line) > 240 {
			driverError(c, 400, "invalid_bullets", "Use at most six short bullet points.")
			return
		}
	}
	if b.Bullets == nil {
		b.Bullets = []string{}
	}
	if b.InterestTags == nil {
		b.InterestTags = []string{}
	}
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		featureFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, e = tx.Exec(c.Request.Context(), `INSERT INTO rider_feature_ideas(key,title,description,interest_tags,created_by,tagline,bullets,question)VALUES($1,$2,$3,$4,$5::uuid,$6,$7,$8)`, b.Key, b.Title, b.Description, b.InterestTags, currentAdmin(c).ID, b.Tagline, b.Bullets, b.Question)
	if e != nil {
		driverError(c, 409, "feature_exists", "This feature key is already in use.")
		return
	}
	e = insertAdminAudit(c, tx, currentAdmin(c).ID, "feature.created", "", "feature", b.Key, gin.H{"reason": b.Reason}, nil, nil)
	if e == nil {
		e = tx.Commit(c.Request.Context())
	}
	if e != nil {
		featureFailure(c)
		return
	}
	heroData(c, gin.H{"key": b.Key})
}
func (a *adminAPI) featurePublish(c *gin.Context) {
	client, e := newIdeasClient()
	if e != nil {
		driverError(c, 503, "ideas_unavailable", "Configure Ideas before publishing.")
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&b) != nil || len(strings.TrimSpace(b.Reason)) < 5 {
		driverError(c, 400, "reason_required", "Supply an audit reason.")
		return
	}
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		featureFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var title, body, state, tagline, question string
	var bullets []string
	var id *int64
	e = tx.QueryRow(c.Request.Context(), `SELECT title,description,status,idea_id,tagline,bullets,question FROM rider_feature_ideas WHERE key=$1 FOR UPDATE`, c.Param("key")).Scan(&title, &body, &state, &id, &tagline, &bullets, &question)
	if e != nil {
		driverError(c, 404, "feature_not_found", "Feature was not found.")
		return
	}
	if state == "disabled" || state == "live" {
		driverError(c, 409, "feature_disabled", "Disabled features cannot be republished.")
		return
	}
	if tagline != "" {
		body = tagline + "\n\n" + body
	}
	for _, line := range bullets {
		body += "\n- " + line
	}
	if question != "" {
		body += "\n\n" + question
	}
	if id == nil {
		var reply struct {
			ID         int64  `json:"id"`
			Moderation string `json:"moderation_state"`
		}
		e = client.Post(c.Request.Context(), "/api/v1/heygo/features/"+c.Param("key"), gin.H{"title": title, "body": body}, &reply)
		if e != nil || reply.ID <= 0 || (reply.Moderation != "" && reply.Moderation != "published") {
			driverError(c, 503, "ideas_unavailable", "Publication wasn't confirmed. Retry safely.")
			return
		}
		id = &reply.ID
	}
	_, e = tx.Exec(c.Request.Context(), `UPDATE rider_feature_ideas SET idea_id=$2,status='published' WHERE key=$1`, c.Param("key"), *id)
	if e == nil {
		e = insertAdminAudit(c, tx, currentAdmin(c).ID, "feature.published", "", "feature", c.Param("key"), gin.H{"reason": b.Reason, "ideaId": *id}, nil, nil)
	}
	if e == nil {
		e = tx.Commit(c.Request.Context())
	}
	if e != nil {
		featureFailure(c)
		return
	}
	heroData(c, gin.H{"ideaId": *id, "status": "published"})
}
func (a *adminAPI) featureDisable(c *gin.Context) {
	var b struct {
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&b) != nil || len(strings.TrimSpace(b.Reason)) < 5 {
		driverError(c, 400, "reason_required", "Supply an audit reason.")
		return
	}
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		featureFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	tag, e := tx.Exec(c.Request.Context(), `UPDATE rider_feature_ideas SET status='disabled' WHERE key=$1`, c.Param("key"))
	if e != nil || tag.RowsAffected() != 1 {
		driverError(c, 404, "feature_not_found", "Feature was not found.")
		return
	}
	e = insertAdminAudit(c, tx, currentAdmin(c).ID, "feature.disabled", "", "feature", c.Param("key"), gin.H{"reason": b.Reason}, nil, nil)
	if e == nil {
		e = tx.Commit(c.Request.Context())
	}
	if e != nil {
		featureFailure(c)
		return
	}
	heroData(c, gin.H{"status": "disabled"})
}

func featureFailure(c *gin.Context) {
	driverError(c, 503, "feature_unavailable", "Feature interest is temporarily unavailable")
}

// Launch is an explicit Admin action, never inferred from an Ideas status.
func (a *adminAPI) featureLaunch(c *gin.Context) {
	var b struct {
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&b) != nil || len(strings.TrimSpace(b.Reason)) < 5 {
		driverError(c, 400, "reason_required", "Supply an audit reason.")
		return
	}
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		featureFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var title, state string
	e = tx.QueryRow(c.Request.Context(), `SELECT title,status FROM rider_feature_ideas WHERE key=$1 FOR UPDATE`, c.Param("key")).Scan(&title, &state)
	if e != nil {
		driverError(c, 404, "feature_not_found", "Feature was not found.")
		return
	}
	if state != "published" && state != "live" {
		driverError(c, 409, "feature_not_published", "Publish the feature first.")
		return
	}
	if state == "live" {
		heroData(c, gin.H{"status": "live"})
		return
	}
	if _, e = tx.Exec(c.Request.Context(), `UPDATE rider_feature_ideas SET status='live' WHERE key=$1`, c.Param("key")); e != nil {
		featureFailure(c)
		return
	}
	rows, e := tx.Query(c.Request.Context(), `SELECT s.rider_id::text FROM rider_feature_subscriptions s JOIN users u ON u.id=s.rider_id WHERE feature_key=$1 AND status='confirmed' AND s.subscribed AND u.active AND 'rider'=ANY(u.roles) ORDER BY s.rider_id`, c.Param("key"))
	if e != nil {
		featureFailure(c)
		return
	}
	owners := []string{}
	for rows.Next() {
		var owner string
		if e = rows.Scan(&owner); e != nil {
			break
		}
		owners = append(owners, owner)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		featureFailure(c)
		return
	}
	for _, owner := range owners {
		if _, e = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text,0))`, owner); e != nil {
			featureFailure(c)
			return
		}
		source := "feature-launch:" + c.Param("key")
		var id string
		e = tx.QueryRow(c.Request.Context(), `INSERT INTO rider_notifications(rider_id,source_event_id,type,title,message,data)VALUES($1::uuid,$2,'account_alerts',$3,$4,jsonb_build_object('featureKey',$5::text)) ON CONFLICT(rider_id,source_event_id) DO UPDATE SET source_event_id=EXCLUDED.source_event_id RETURNING id::text`, owner, source, title+" is now available", "You asked us to let you know. Open HeyGo to explore this service.", c.Param("key")).Scan(&id)
		if e != nil {
			featureFailure(c)
			return
		}
		data, _ := json.Marshal(gin.H{"notificationId": id})
		if _, e = messaging.AppendEventTx(c.Request.Context(), tx, owner, source+":created", "rider.notification.created", data); e != nil {
			featureFailure(c)
			return
		}
		_, e = tx.Exec(c.Request.Context(), `INSERT INTO rider_notification_push_deliveries(notification_id,device_id) SELECT $1::bigint,d.id FROM rider_devices d JOIN user_sessions ss ON ss.id=d.session_id LEFT JOIN rider_notification_preferences p ON p.rider_id=d.rider_id WHERE d.rider_id=$2::uuid AND d.enabled AND ss.revoked_at IS NULL AND ss.refresh_expires_at>NOW() AND COALESCE(p.account_alerts,TRUE) ON CONFLICT DO NOTHING`, id, owner)
		if e != nil {
			featureFailure(c)
			return
		}
	}
	e = insertAdminAudit(c, tx, currentAdmin(c).ID, "feature.launched", "", "feature", c.Param("key"), gin.H{"reason": b.Reason, "recipients": len(owners)}, nil, nil)
	if e == nil {
		e = tx.Commit(c.Request.Context())
	}
	if e != nil {
		featureFailure(c)
		return
	}
	heroData(c, gin.H{"status": "live", "notificationsQueued": len(owners)})
}

func (a *featureInterestAPI) unsubscribe(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	u, _ := gatewayauth.CurrentUser(c)
	_, e := a.pool.Exec(c.Request.Context(), `UPDATE rider_feature_subscriptions SET subscribed=FALSE WHERE feature_key=$1 AND rider_id=$2::uuid`, c.Param("key"), u.ID)
	if e != nil {
		featureFailure(c)
		return
	}
	heroData(c, gin.H{"subscribed": false})
}
