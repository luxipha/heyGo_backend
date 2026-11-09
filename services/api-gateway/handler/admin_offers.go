package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/offers"
	"strings"
)

func (a *adminAPI) offerRule(c *gin.Context) {
	id, ok := campaignID(c)
	if !ok {
		return
	}
	r, e := offers.Read(c.Request.Context(), a.pool, id.String())
	if errors.Is(e, pgx.ErrNoRows) {
		heroData(c, gin.H{"offer": nil, "enabled": offers.Enabled()})
		return
	}
	if e != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"offer": r, "enabled": offers.Enabled()})
}
func (a *adminAPI) activateOffer(c *gin.Context) {
	if !offers.Enabled() {
		driverError(c, 409, "offers_disabled", "Offer execution is disabled for this environment.")
		return
	}
	id, ok := campaignID(c)
	if !ok {
		return
	}
	var b struct {
		offers.Rule
		Version int    `json:"version"`
		Reason  string `json:"reason"`
	}
	if c.ShouldBindJSON(&b) != nil || b.Version < 1 || len(strings.TrimSpace(b.Reason)) < 5 || len(b.Reason) > 1000 {
		driverError(c, 400, "invalid_offer", "Set offer rules and an audit reason.")
		return
	}
	b.Code = offers.NormalizeCode(b.Code)
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, e = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext('publish-rider-offer'))`)
	if e != nil {
		heroFailure(c)
		return
	}
	var kind, audience, category string
	var version int
	e = tx.QueryRow(c.Request.Context(), `SELECT kind,audience,category,name,body,budget_kobo,version FROM admin_campaign_drafts WHERE id=$1 FOR UPDATE`, id).Scan(&kind, &audience, &category, &b.Title, &b.Message, &b.Budget, &version)
	if e != nil {
		driverError(c, 404, "campaign_not_found", "Campaign not found.")
		return
	}
	if kind != "promotion" || audience != "Riders" || version != b.Version || (b.Kind == "discount" && category != "Discount") || (b.Kind == "gocoin_multiplier" && category != "Incentive") {
		driverError(c, 409, "offer_draft_changed", "Use the current Riders Discount/Incentive draft and matching offer kind.")
		return
	}
	if e = offers.Validate(b.Rule); e != nil {
		driverError(c, 400, "invalid_offer", e.Error())
		return
	}
	var count int
	if e = tx.QueryRow(c.Request.Context(), `SELECT count(*) FROM rider_offers WHERE active AND ends_at>NOW()`).Scan(&count); e != nil || count >= 100 {
		driverError(c, 409, "offer_limit", "At most 100 current offers can be published.")
		return
	}
	for _, slug := range b.Categories {
		var exists bool
		if tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM ride_categories WHERE slug=$1 AND status='active')`, slug).Scan(&exists) != nil || !exists {
			driverError(c, 400, "invalid_category", "Use active category slugs.")
			return
		}
	}
	for _, market := range b.Markets {
		var exists bool
		if tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM trip_geofences WHERE kind='market' AND code=$1 AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW()))`, market).Scan(&exists) != nil || !exists {
			driverError(c, 400, "invalid_market", "Use active market codes.")
			return
		}
	}
	b.ID = id.String()
	b.Active = true
	_, e = tx.Exec(c.Request.Context(), `INSERT INTO rider_offers(id,title,message,kind,discount_kobo,multiplier,code,starts_at,ends_at,market_codes,category_slugs,airport_only,start_hour,end_hour,timezone,budget_kobo,max_uses,per_rider_limit,created_by)VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19::uuid)`, id, b.Title, b.Message, b.Kind, b.Discount, b.Multiplier, b.Code, b.Starts, b.Ends, b.Markets, b.Categories, b.AirportOnly, b.StartHour, b.EndHour, b.Timezone, b.Budget, b.MaxUses, b.PerRider, currentAdmin(c).ID)
	if e != nil {
		driverError(c, 409, "offer_conflict", "This draft is already published or its code is in use.")
		return
	}
	if e = adminAudit(c, tx, "promotion.activated", id.String(), gin.H{"reason": b.Reason, "kind": b.Kind, "budgetKobo": b.Budget, "hasCode": b.Code != ""}); e == nil {
		e = tx.Commit(c.Request.Context())
	}
	if e != nil {
		heroFailure(c)
		return
	}
	heroData(c, b.Rule)
}
func (a *adminAPI) disableOffer(c *gin.Context) {
	id, ok := campaignID(c)
	if !ok {
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&b) != nil || len(strings.TrimSpace(b.Reason)) < 5 || len(b.Reason) > 1000 {
		driverError(c, 400, "reason_required", "Supply an audit reason.")
		return
	}
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	tag, e := tx.Exec(c.Request.Context(), `UPDATE rider_offers SET active=false WHERE id=$1`, id)
	if e != nil || tag.RowsAffected() != 1 {
		driverError(c, 404, "offer_not_found", "Offer not found.")
		return
	}
	if e = adminAudit(c, tx, "promotion.disabled", id.String(), gin.H{"reason": b.Reason}); e == nil {
		e = tx.Commit(c.Request.Context())
	}
	if e != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"active": false})
}
