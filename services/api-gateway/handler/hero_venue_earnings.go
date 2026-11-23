package handler

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/env"
)

// partnerAuthorized checks the shared key the venue platform signs its reports with.
// Server-to-server only; fails closed when the key is not configured.
func partnerAuthorized(c *gin.Context) bool {
	key := env.GetString("VENUE_PARTNER_INBOUND_KEY", "")
	if len(key) < 32 {
		driverError(c, http.StatusServiceUnavailable, "partner_unavailable", "Partner reporting is not configured")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(c.GetHeader("x-service-key")), []byte(key)) != 1 {
		driverError(c, http.StatusUnauthorized, "partner_unauthorized", "Invalid service credentials")
		return false
	}
	return true
}

// registerVenueEarningsPartnerRoute lets the venue platform report what a Hero's
// venues earned. Repeats are ignored, so the venue platform can safely retry.
func registerVenueEarningsPartnerRoute(r *gin.Engine, pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	// A delivered order: the Hero's share of the platform fee. The first delivery also
	// queues the go-live bonus, and reaching N deliveries queues the orders bonus.
	r.POST("/partner/venue-earnings", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
		if !partnerAuthorized(c) {
			return
		}
		var b struct {
			HeroID         string `json:"heroId"`
			OrderID        string `json:"orderId"`
			VenueName      string `json:"venueName"`
			VenueRef       string `json:"venueRef"`
			AmountKobo     int64  `json:"amountKobo"`
			PlatformFee    int64  `json:"platformFeeKobo"`
			DeliveredAtRaw string `json:"deliveredAt"`
		}
		if c.ShouldBindJSON(&b) != nil {
			driverError(c, http.StatusBadRequest, "invalid_earning", "Invalid earning")
			return
		}
		b.OrderID = strings.TrimSpace(b.OrderID)
		b.VenueRef = strings.TrimSpace(b.VenueRef)
		if _, err := uuid.Parse(b.HeroID); err != nil || b.OrderID == "" || len(b.OrderID) > 80 || len(b.VenueRef) > 80 || b.AmountKobo <= 0 || b.AmountKobo > 100000000 || b.PlatformFee < b.AmountKobo {
			driverError(c, http.StatusBadRequest, "invalid_earning", "Invalid earning")
			return
		}
		var delivered *time.Time
		if t, err := time.Parse(time.RFC3339, b.DeliveredAtRaw); err == nil {
			delivered = &t
		}
		ctx := c.Request.Context()
		tx, err := pool.Begin(ctx)
		if err != nil {
			heroFailure(c)
			return
		}
		defer tx.Rollback(ctx)
		tag, err := tx.Exec(ctx, `INSERT INTO hero_venue_earnings(id,hero_id,partner_order_id,venue_name,venue_ref,amount_kobo,platform_fee_kobo,delivered_at)
SELECT $1::uuid,h.id,$3,$4,NULLIF($8,''),$5,$6,$7 FROM heroes_accounts h WHERE h.id=$2::uuid ON CONFLICT(partner_order_id)DO NOTHING`,
			uuid.NewString(), b.HeroID, b.OrderID, strings.TrimSpace(b.VenueName), b.AmountKobo, b.PlatformFee, delivered, b.VenueRef)
		if err != nil {
			heroFailure(c)
			return
		}
		if tag.RowsAffected() == 1 && b.VenueRef != "" {
			// Count this venue's delivered orders under the Hero's lock so two orders
			// arriving together cannot both miss (or both create) the bonus.
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "hero-venue-bonus:"+b.VenueRef); err != nil {
				heroFailure(c)
				return
			}
			// Bonuses are only ever queued by a delivered order: the go-live bonus with the
			// venue's first delivery, the orders bonus when it reaches N deliveries.
			_, err = tx.Exec(ctx, `INSERT INTO hero_venue_earnings(id,hero_id,partner_order_id,venue_name,venue_ref,amount_kobo,kind,delivered_at)
SELECT gen_random_uuid(),$1::uuid,'live:'||$2,$3,$2,s.live_bonus_kobo,'live_bonus',now() FROM heroes_settings s
WHERE s.id=1 AND s.live_bonus_kobo>0
ON CONFLICT(partner_order_id)DO NOTHING`, b.HeroID, b.VenueRef, strings.TrimSpace(b.VenueName))
			if err != nil {
				heroFailure(c)
				return
			}
			_, err = tx.Exec(ctx, `INSERT INTO hero_venue_earnings(id,hero_id,partner_order_id,venue_name,venue_ref,amount_kobo,kind,delivered_at)
SELECT gen_random_uuid(),$1::uuid,'orders:'||$2,$3,$2,s.orders_bonus_kobo,'orders_bonus',now() FROM heroes_settings s
WHERE s.id=1 AND s.orders_bonus_kobo>0
 AND (SELECT count(*) FROM hero_venue_earnings e WHERE e.venue_ref=$2 AND e.kind='order_share' AND e.status<>'rejected')>=s.orders_bonus_count
ON CONFLICT(partner_order_id)DO NOTHING`, b.HeroID, b.VenueRef, strings.TrimSpace(b.VenueName))
			if err != nil {
				heroFailure(c)
				return
			}
		}
		if err = tx.Commit(ctx); err != nil {
			heroFailure(c)
			return
		}
		// Unknown Hero: acknowledge nothing was stored so the venue platform does not retry forever.
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"recorded": tag.RowsAffected() == 1}})
	})
}

func (a *adminAPI) venueEarnings(c *gin.Context) {
	status := c.DefaultQuery("status", "pending")
	if status != "pending" && status != "approved" && status != "rejected" {
		driverError(c, 400, "invalid_status", "Invalid status")
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `SELECT e.id::text,e.hero_id::text,h.name,e.venue_name,e.partner_order_id,e.amount_kobo,e.platform_fee_kobo,e.delivered_at,e.status,e.reject_reason,e.created_at,e.kind FROM hero_venue_earnings e JOIN heroes_accounts h ON h.id=e.hero_id WHERE e.status=$1 ORDER BY e.created_at DESC LIMIT 200`, status)
	if err != nil {
		heroFailure(c)
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, hero, name, venue, order, st, kind string
		var amount, fee int64
		var delivered *time.Time
		var reason *string
		var created time.Time
		if rows.Scan(&id, &hero, &name, &venue, &order, &amount, &fee, &delivered, &st, &reason, &created, &kind) != nil {
			heroFailure(c)
			return
		}
		items = append(items, gin.H{"id": id, "heroId": hero, "hero": name, "venue": venue, "orderId": order, "amountKobo": amount, "platformFeeKobo": fee, "deliveredAt": delivered, "status": st, "rejectReason": reason, "createdAt": created, "kind": kind})
	}
	if rows.Err() != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"items": items})
}

func (a *adminAPI) approveVenueEarnings(c *gin.Context) {
	var b struct {
		IDs []string `json:"ids"`
	}
	if c.ShouldBindJSON(&b) != nil || len(b.IDs) < 1 || len(b.IDs) > 100 {
		driverError(c, 400, "invalid_earnings", "Select up to 100 earnings")
		return
	}
	for _, id := range b.IDs {
		if _, err := uuid.Parse(id); err != nil {
			driverError(c, 400, "invalid_earnings", "Select up to 100 earnings")
			return
		}
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	approved := 0
	for _, id := range b.IDs {
		var hero, kind string
		var amount int64
		// Locked and re-checked in this transaction: an earning can only be approved once.
		err = tx.QueryRow(ctx, `UPDATE hero_venue_earnings SET status='approved',reviewed_by=$2::uuid,reviewed_at=now() WHERE id=$1::uuid AND status='pending' RETURNING hero_id::text,amount_kobo,kind`, id, currentAdmin(c).ID).Scan(&hero, &amount, &kind)
		if err == pgx.ErrNoRows {
			continue
		}
		if err != nil {
			heroFailure(c)
			return
		}
		if _, err = tx.Exec(ctx, `INSERT INTO heroes_wallet_entries(id,hero_id,venue_earning_id,amount_kobo,kind)VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5)`, uuid.NewString(), hero, id, amount, walletKindFor(kind)); err != nil {
			heroFailure(c)
			return
		}
		approved++
	}
	if err = adminAudit(c, tx, "heroes.venue_earnings.approved", "", gin.H{"ids": b.IDs, "approved": approved}); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"approved": approved})
}

func (a *adminAPI) rejectVenueEarnings(c *gin.Context) {
	var b struct {
		IDs    []string `json:"ids"`
		Reason string   `json:"reason"`
	}
	b.Reason = ""
	if c.ShouldBindJSON(&b) != nil || len(b.IDs) < 1 || len(b.IDs) > 100 || len(strings.TrimSpace(b.Reason)) < 3 || len(b.Reason) > 300 {
		driverError(c, 400, "invalid_earnings", "Select earnings and give a reason")
		return
	}
	for _, id := range b.IDs {
		if _, err := uuid.Parse(id); err != nil {
			driverError(c, 400, "invalid_earnings", "Select earnings and give a reason")
			return
		}
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	var rejected int64
	for _, id := range b.IDs {
		tag, err := tx.Exec(ctx, `UPDATE hero_venue_earnings SET status='rejected',reviewed_by=$2::uuid,reviewed_at=now(),reject_reason=$3 WHERE id=$1::uuid AND status='pending'`, id, currentAdmin(c).ID, strings.TrimSpace(b.Reason))
		if err != nil {
			heroFailure(c)
			return
		}
		rejected += tag.RowsAffected()
	}
	if err = adminAudit(c, tx, "heroes.venue_earnings.rejected", "", gin.H{"ids": b.IDs, "rejected": rejected}); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"rejected": rejected})
}

// Order shares and bonuses share one approval queue but show differently in the wallet.
func walletKindFor(earningKind string) string {
	if earningKind == "order_share" {
		return "venue_order_share"
	}
	return "venue_bonus"
}
