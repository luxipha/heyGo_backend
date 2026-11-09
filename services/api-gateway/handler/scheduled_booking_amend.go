package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"time"
)

type scheduledEditInput struct {
	RequestID string    `json:"requestId"`
	Version   int64     `json:"version"`
	FareID    string    `json:"fareId"`
	PickupAt  time.Time `json:"pickupAt"`
	Confirmed bool      `json:"confirmed"`
}

// Registered only after integration gates verify ownership, rollback and retry.
func (a *scheduledAPI) amend(c *gin.Context) {
	var b scheduledEditInput
	id := c.Param("bookingID")
	if uuid.Validate(id) != nil || c.ShouldBindJSON(&b) != nil || uuid.Validate(b.RequestID) != nil || uuid.Validate(b.FareID) != nil || b.Version < 1 || b.PickupAt.IsZero() || !b.Confirmed {
		driverError(c, 400, "invalid_schedule_amendment", "Confirm a fresh fare, booking version, request UUID and pickup time.")
		return
	}
	b.PickupAt = b.PickupAt.UTC().Truncate(time.Microsecond)
	b.FareID = uuid.MustParse(b.FareID).String()
	ctx := c.Request.Context()
	u, _ := gatewayauth.CurrentUser(c)
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	var active bool
	e = tx.QueryRow(ctx, `SELECT active AND NOT rider_trip_restricted FROM users WHERE id=$1::uuid FOR UPDATE`, u.ID).Scan(&active)
	if e != nil || !active {
		driverError(c, 403, "rider_restricted", "Booking edits are unavailable for this account.")
		return
	}
	var oldFare, state, market string
	var version int64
	var deadline time.Time
	var driver *string
	e = tx.QueryRow(ctx, `SELECT fare_id::text,status,market_code,version,free_cancellation_until,driver_id::text FROM scheduled_rides WHERE id=$1::uuid AND rider_id=$2::uuid FOR UPDATE`, id, u.ID).Scan(&oldFare, &state, &market, &version, &deadline, &driver)
	if e != nil {
		scheduleFail(c, e)
		return
	}
	var savedFare string
	var savedAt time.Time
	var savedVersion int64
	e = tx.QueryRow(ctx, `SELECT new_fare_id::text,pickup_at,resulting_version FROM scheduled_ride_amendments WHERE booking_id=$1::uuid AND request_id=$2::uuid`, id, b.RequestID).Scan(&savedFare, &savedAt, &savedVersion)
	if e == nil {
		if savedFare != b.FareID || !savedAt.Equal(b.PickupAt) {
			driverError(c, 409, "schedule_conflict", "Request ID already used with different edits.")
			return
		}
		heroData(c, gin.H{"bookingId": id, "version": savedVersion, "refreshScheduledRides": true})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		heroFailure(c)
		return
	}
	if version != b.Version || (state != "open" && state != "reserved") || !deadline.After(time.Now()) {
		driverError(c, 409, "schedule_edit_closed", "Booking changed or the editing deadline passed. Refresh the booking.")
		return
	}
	p, e := readSchedulePolicy(ctx, tx)
	now := time.Now().UTC()
	if e != nil || !p.Enabled || b.PickupAt.Before(now.Add(time.Duration(p.MinAdvance)*time.Minute)) || b.PickupAt.After(now.Add(time.Duration(p.MaxAdvance)*time.Minute)) {
		driverError(c, 409, "schedule_window_unavailable", "Pickup is outside the booking window.")
		return
	}
	region, destinationRegion, seconds, e := replacementScheduledFare(ctx, tx, b.FareID, u.ID, market, b.PickupAt, p)
	if e != nil {
		driverError(c, 409, "fare_requote_required", "Get a fresh quote for this market and pickup time.")
		return
	}
	e = applyScheduledEdit(ctx, tx, id, oldFare, driver, version, b, p, region, destinationRegion, seconds)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		scheduleFail(c, e)
		return
	}
	heroData(c, gin.H{"bookingId": id, "version": version + 1, "reservationReleased": driver != nil, "refreshScheduledRides": true})
}
