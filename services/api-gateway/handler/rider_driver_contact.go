package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

// A separate, on-demand read keeps contact details out of snapshots, history
// and lifecycle broadcasts. A direct number is disclosed to the calling Rider.
func (a *riderHomeAPI) driverContact(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Pragma", "no-cache")
	id := c.Param("tripID")
	if _, err := uuid.Parse(id); err != nil {
		driverError(c, 400, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	user, ok := gatewayauth.CurrentUser(c)
	if !ok {
		driverError(c, 401, "unauthorized", "Sign in to contact your Driver")
		return
	}
	if a.pool == nil {
		driverError(c, 503, "driver_contact_unavailable", "Driver contact is unavailable")
		return
	}
	var phone *string
	err := a.pool.QueryRow(c.Request.Context(), `SELECT CASE WHEN t.status IN ('accepted','arrived','started') AND d.active AND 'driver'=ANY(d.roles) AND d.phone_verified_at IS NOT NULL THEN NULLIF(d.phone_e164,'') ELSE NULL END FROM trips t LEFT JOIN users d ON d.id=t.assigned_driver_id WHERE t.id=$1::uuid AND t.rider_id=$2::uuid`, id, user.ID).Scan(&phone)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "trip_not_found", "Trip was not found")
		return
	}
	if err != nil {
		driverError(c, 503, "driver_contact_unavailable", "Driver contact is unavailable")
		return
	}
	var contact any
	if phone != nil {
		// Never return malformed data as a dialable phone number.
		if normalized, valid := normalizePhone(*phone); valid && normalized == *phone {
			contact = gin.H{"kind": "direct", "phoneNumber": normalized}
		}
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"tripId": id, "contact": contact}})
}
