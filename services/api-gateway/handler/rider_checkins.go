package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
)

func (a *riderAmendmentAPI) checkins(c *gin.Context) {
	if _, err := uuid.Parse(c.Param("tripID")); err != nil {
		driverError(c, 400, "invalid_trip_id", "Valid trip ID required")
		return
	}
	if a.pool == nil {
		accountError(c)
		return
	}
	var owned bool
	if a.pool.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM trips WHERE id=$1::uuid AND rider_id=$2::uuid)`, c.Param("tripID"), riderAccountOwner(c)).Scan(&owned) != nil {
		accountError(c)
		return
	}
	if !owned {
		driverError(c, 404, "trip_not_found", "Trip was not found")
		return
	}
	var raw []byte
	err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE(jsonb_agg(item ORDER BY created DESC),'[]') FROM (SELECT c.created_at created,jsonb_build_object('id',c.id,'tripId',c.trip_id,'kind',c.kind,'routeRevision',c.route_revision,'observedAt',c.observed_at,'response',c.response,'respondedAt',c.responded_at,'actionable',c.response IS NULL AND t.status='started' AND t.route_revision=c.route_revision) item FROM trip_safety_checkins c JOIN trips t ON t.id=c.trip_id WHERE c.trip_id=$1::uuid AND c.rider_id=$2::uuid ORDER BY c.created_at DESC LIMIT 20) q`, c.Param("tripID"), riderAccountOwner(c)).Scan(&raw)
	if err != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"items": json.RawMessage(raw)}})
}
func (a *riderAmendmentAPI) respondCheckin(c *gin.Context) {
	var b struct {
		Response string `json:"response"`
	}
	if _, err := uuid.Parse(c.Param("tripID")); err != nil {
		driverError(c, 400, "invalid_trip_id", "Valid trip ID required")
		return
	}
	if _, err := uuid.Parse(c.Param("checkinID")); err != nil || c.ShouldBindJSON(&b) != nil || b.Response != "okay" && b.Response != "need_help" {
		driverError(c, 400, "invalid_response", "Choose okay or need_help")
		return
	}
	if a.pool == nil {
		accountError(c)
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(ctx)
	var status string
	var revision int64
	if tx.QueryRow(ctx, `SELECT status,route_revision FROM trips WHERE id=$1::uuid AND rider_id=$2::uuid FOR UPDATE`, c.Param("tripID"), riderAccountOwner(c)).Scan(&status, &revision) != nil {
		driverError(c, 404, "trip_not_found", "Trip was not found")
		return
	}
	var response *string
	var current int64
	if tx.QueryRow(ctx, `SELECT response,route_revision FROM trip_safety_checkins WHERE id=$1::uuid AND trip_id=$2::uuid AND rider_id=$3::uuid FOR UPDATE`, c.Param("checkinID"), c.Param("tripID"), riderAccountOwner(c)).Scan(&response, &current) != nil {
		driverError(c, 404, "checkin_not_found", "Check-in was not found")
		return
	}
	if response != nil && *response != b.Response || response == nil && (status != "started" || current != revision) {
		driverError(c, 409, "checkin_changed", "Check-in is answered or no longer current")
		return
	}
	if response == nil {
		_, err = tx.Exec(ctx, `UPDATE trip_safety_checkins SET response=$2,responded_at=NOW() WHERE id=$1::uuid`, c.Param("checkinID"), b.Response)
		raw, _ := json.Marshal(gin.H{"tripId": c.Param("tripID"), "checkinId": c.Param("checkinID"), "response": b.Response})
		if err == nil {
			_, err = messaging.AppendEventTx(ctx, tx, riderAccountOwner(c), "checkin-response:"+c.Param("checkinID"), "trip.event.safety_checkin_responded", raw)
		}
	}
	if err != nil || tx.Commit(ctx) != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"recorded": true, "response": b.Response, "showSafetyActions": b.Response == "need_help", "emergencyDispatched": false}})
}
