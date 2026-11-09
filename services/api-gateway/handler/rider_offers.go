package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/offers"
	"time"
)

func registerRiderOffers(g *gin.RouterGroup, pool *pgxpool.Pool) {
	a := &riderOffersAPI{pool: pool}
	r := g.Group("/rider/offers", gatewayauth.RequireRole("rider"))
	r.GET("", a.list)
	r.POST("/claim", a.claim)
}

type riderOffersAPI struct{ pool *pgxpool.Pool }

func publicOffer(r offers.Rule) gin.H {
	return gin.H{"id": r.ID, "title": r.Title, "message": r.Message, "kind": r.Kind, "discountKobo": r.Discount, "multiplier": r.Multiplier, "startsAt": r.Starts, "endsAt": r.Ends, "markets": r.Markets, "categories": r.Categories, "airportOnly": r.AirportOnly, "startHour": r.StartHour, "endHour": r.EndHour, "timezone": r.Timezone, "requiresCode": r.Code != "", "immediateRidesOnly": true}
}
func (a *riderOffersAPI) list(c *gin.Context) {
	if !offers.Enabled() {
		heroData(c, gin.H{"enabled": false, "items": []any{}})
		return
	}
	u, _ := gatewayauth.CurrentUser(c)
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	rows, e := tx.Query(c.Request.Context(), `SELECT `+offers.Columns+` FROM rider_offers WHERE active AND starts_at<=NOW() AND ends_at>NOW() AND (code IS NULL OR EXISTS(SELECT 1 FROM rider_offer_claims WHERE offer_id=rider_offers.id AND rider_id=$1::uuid)) ORDER BY ends_at,id LIMIT 100`, u.ID)
	if e != nil {
		heroFailure(c)
		return
	}
	rules := []offers.Rule{}
	for rows.Next() {
		r, e := offers.Scan(rows)
		if e != nil {
			rows.Close()
			heroFailure(c)
			return
		}
		rules = append(rules, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		heroFailure(c)
		return
	}
	items := []gin.H{}
	for _, r := range rules {
		ok, e := offers.Capacity(c.Request.Context(), tx, r, u.ID, 0)
		if e != nil {
			heroFailure(c)
			return
		}
		if ok {
			items = append(items, publicOffer(r))
		}
	}
	c.Header("Cache-Control", "no-store")
	heroData(c, gin.H{"enabled": true, "items": items, "application": "best_one_per_kind", "note": "Applied to eligible immediate rides automatically. Availability is rechecked at booking."})
}
func (a *riderOffersAPI) claim(c *gin.Context) {
	var b struct {
		Code string `json:"code"`
	}
	if !offers.Enabled() {
		driverError(c, 409, "offers_disabled", "Offers are unavailable right now.")
		return
	}
	if c.ShouldBindJSON(&b) != nil || !offers.ValidCode(offers.NormalizeCode(b.Code)) {
		driverError(c, 400, "invalid_promo_code", "Enter a valid promo code.")
		return
	}
	u, _ := gatewayauth.CurrentUser(c)
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	r, e := offers.Scan(tx.QueryRow(c.Request.Context(), `SELECT `+offers.Columns+` FROM rider_offers WHERE code=$1 AND active AND starts_at<=NOW() AND ends_at>NOW() FOR UPDATE`, offers.NormalizeCode(b.Code)))
	if e != nil || !r.Active || !r.Ends.After(time.Now()) {
		driverError(c, 409, "promo_code_unavailable", "This promo code is invalid, expired or unavailable.")
		return
	}
	ok, e := offers.Capacity(c.Request.Context(), tx, r, u.ID, 0)
	if e != nil || !ok {
		driverError(c, 409, "promo_code_unavailable", "This promo code is no longer available for your account.")
		return
	}
	_, e = tx.Exec(c.Request.Context(), `INSERT INTO rider_offer_claims(offer_id,rider_id)VALUES($1::uuid,$2::uuid)ON CONFLICT DO NOTHING`, r.ID, u.ID)
	if e == nil {
		e = tx.Commit(c.Request.Context())
	}
	if e != nil {
		heroFailure(c)
		return
	}
	heroData(c, publicOffer(r))
}
