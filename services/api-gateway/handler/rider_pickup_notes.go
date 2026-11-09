package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"strings"
	"unicode"
)

func normalizePickupNote(raw string) (string, bool) {
	note := strings.TrimSpace(raw)
	return note, len([]rune(note)) <= 500 && !strings.ContainsFunc(note, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' })
}

func registerRiderTripOptions(g *gin.RouterGroup, pool *pgxpool.Pool) {
	api := &riderAmendmentAPI{pool: pool}
	r := g.Group("/rider", gatewayauth.RequireRole("rider"))
	r.PUT("/fares/:fareID/preferences", setRiderPreferences(pool, false))
	r.PUT("/trips/:tripID/preferences", setRiderPreferences(pool, true))
	r.PUT("/fares/:fareID/guest", setFareGuestPassenger(pool))
	r.PUT("/fares/:fareID/pickup-note", func(c *gin.Context) {
		var body struct {
			Note string `json:"pickupNote"`
		}
		id, err := uuid.Parse(c.Param("fareID"))
		if err != nil || c.ShouldBindJSON(&body) != nil {
			driverError(c, 400, "invalid_pickup_note", "A valid fare and pickup note are required")
			return
		}
		note, ok := normalizePickupNote(body.Note)
		if !ok {
			driverError(c, 400, "invalid_pickup_note", "Pickup notes allow at most 500 characters without control characters")
			return
		}
		if pool == nil {
			accountError(c)
			return
		}
		result, err := pool.Exec(c.Request.Context(), `UPDATE ride_fares SET pickup_note=$3 WHERE id=$1::uuid AND rider_id=$2::uuid AND consumed_at IS NULL AND expires_at>NOW()`, id, riderAccountOwner(c), note)
		if err != nil {
			accountError(c)
			return
		}
		if result.RowsAffected() != 1 {
			driverError(c, 409, "fare_unavailable", "Refresh the quote before editing pickup notes")
			return
		}
		c.JSON(200, contracts.APIResponse{Data: gin.H{"fareId": id, "pickupNote": note}})
	})
	r.POST("/trips/:tripID/amendments/preview", api.preview)
	r.POST("/trips/:tripID/amendments/:quoteID/confirm", api.confirm)
	r.GET("/trips/:tripID/check-ins", api.checkins)
	r.POST("/trips/:tripID/check-ins/:checkinID/respond", api.respondCheckin)
	g.POST("/driver/trips/:tripID/stops/:stopID/complete", gatewayauth.RequireRole("driver"), api.completeStop)
}
