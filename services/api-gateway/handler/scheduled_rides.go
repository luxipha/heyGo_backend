package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/luxipha/heyGo_backend/shared/rideridentity"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"github.com/luxipha/heyGo_backend/shared/messaging"
)

type schedulePolicy struct {
	AmendmentsEnabled bool     `json:"amendmentsEnabled"`
	Version           int64    `json:"version"`
	Enabled           bool     `json:"enabled"`
	Markets           []string `json:"markets"`
	Categories        []string `json:"categories"`
	MinAdvance        int      `json:"minAdvanceMinutes"`
	MaxAdvance        int      `json:"maxAdvanceMinutes"`
	Dispatch          int      `json:"dispatchMinutes"`
	Readiness         int      `json:"readinessMinutes"`
	FreeCancellation  int      `json:"freeCancellationMinutes"`
	PickupWindow      int      `json:"pickupWindowMinutes"`
	RiderReminder     int      `json:"riderReminderMinutes"`
}
type scheduleDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readSchedulePolicy(ctx context.Context, q scheduleDB) (schedulePolicy, error) {
	var p schedulePolicy
	p.AmendmentsEnabled = scheduledBookingEditsEnabled()
	e := q.QueryRow(ctx, `SELECT version,enabled,market_codes,category_slugs,min_advance_minutes,max_advance_minutes,dispatch_minutes,readiness_minutes,free_cancellation_minutes,pickup_window_minutes,rider_reminder_minutes FROM scheduled_ride_policies ORDER BY version DESC LIMIT 1`).Scan(&p.Version, &p.Enabled, &p.Markets, &p.Categories, &p.MinAdvance, &p.MaxAdvance, &p.Dispatch, &p.Readiness, &p.FreeCancellation, &p.PickupWindow, &p.RiderReminder)
	return p, e
}

// Explicit rollout gate: off by default until the deployment is validated.
func scheduledBookingEditsEnabled() bool {
	return os.Getenv("HEYGO_SCHEDULE_EDITS_ENABLED") == "true"
}
func scheduleFail(c *gin.Context, e error) {
	if errors.Is(e, pgx.ErrNoRows) {
		driverError(c, 404, "schedule_not_found", "Scheduled ride was not found")
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(e, &pgErr) && pgErr.Code != "23P01" && pgErr.Code != "23505" {
		slog.Warn("Scheduled ride database operation failed", "code", pgErr.Code)
		driverError(c, 503, "schedule_unavailable", "Scheduled rides are temporarily unavailable. Try again.")
		return
	}
	driverError(c, 409, "schedule_unavailable", "Refresh this booking. Availability, timing, eligibility or another commitment may have changed.")
}
func scheduleEvent(ctx context.Context, tx pgx.Tx, id, kind string, extra ...string) error {
	var raw []byte
	var rider string
	var driver *string
	var version int64
	e := tx.QueryRow(ctx, `SELECT rider_id::text,driver_id::text,version,jsonb_build_object('bookingId',id,'status',status,'pickupAt',pickup_at,'version',version,'tripId',trip_id,'refreshScheduledRides',true) FROM scheduled_rides WHERE id=$1::uuid`, id).Scan(&rider, &driver, &version, &raw)
	if e != nil {
		return e
	}
	recipients := append([]string{rider}, extra...)
	if driver != nil && kind != "scheduled.event.rider_reminder" {
		recipients = append(recipients, *driver)
	}
	sort.Strings(recipients)
	previous := ""
	for _, recipient := range recipients {
		if recipient == previous {
			continue
		}
		previous = recipient
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		payload["audience"] = "driver"
		if recipient == rider {
			payload["audience"] = "rider"
		}
		data, _ := json.Marshal(payload)
		if _, e = messaging.AppendEventTx(ctx, tx, recipient, fmt.Sprintf("schedule:%s:%d:%s", id, version, recipient), kind, data); e != nil {
			return e
		}
	}
	return nil
}
func registerScheduledRoutes(group *gin.RouterGroup, pool *pgxpool.Pool) {
	a := &scheduledAPI{pool: pool, router: geo.DatabaseRoutes{Pool: pool}}
	r := group.Group("/rider/scheduled-rides", gatewayauth.RequireRole("rider"))
	r.GET("/config", func(c *gin.Context) {
		p, e := readSchedulePolicy(c.Request.Context(), pool)
		if e != nil {
			heroFailure(c)
			return
		}
		heroData(c, p)
	})
	r.POST("", a.create)
	r.GET("", func(c *gin.Context) { a.list(c, false) })
	r.GET("/:bookingID", func(c *gin.Context) { a.list(c, false) })
	r.POST("/:bookingID/cancel", a.cancel)
	r.POST("/:bookingID/amend", func(c *gin.Context) {
		if !scheduledBookingEditsEnabled() {
			driverError(c, 409, "schedule_edits_disabled", "Booking edits are unavailable right now.")
			return
		}
		a.amend(c)
	})
	r.PUT("/:bookingID/reminder", a.reminder)
	d := group.Group("/driver/scheduled-rides", gatewayauth.RequireRole("driver"))
	d.GET("", func(c *gin.Context) { a.list(c, true) })
	d.GET("/:bookingID", func(c *gin.Context) { a.list(c, true) })
	d.POST("/:bookingID/accept", a.accept)
	d.POST("/:bookingID/decline", a.decline)
	d.POST("/:bookingID/release", a.release)
	d.POST("/:bookingID/ready", a.ready)
}

type scheduledAPI struct {
	pool   *pgxpool.Pool
	router geo.RouteService
}

func (a *scheduledAPI) create(c *gin.Context) {
	var b struct {
		ServiceType   string    `json:"serviceType"`
		RequestID     string    `json:"requestId"`
		FareID        string    `json:"fareId"`
		PickupAt      time.Time `json:"pickupAt"`
		Confirmed     bool      `json:"confirmed"`
		PaymentMethod string    `json:"paymentMethod"`
	}
	if c.ShouldBindJSON(&b) != nil || (b.ServiceType != "" && b.ServiceType != "local") || (b.PaymentMethod != "" && b.PaymentMethod != "direct_to_driver") || uuid.Validate(b.RequestID) != nil || uuid.Validate(b.FareID) != nil || !b.Confirmed || b.PickupAt.IsZero() {
		driverError(c, 400, "invalid_schedule", "Confirm a quoted fare, request UUID and pickup time with a time-zone offset.")
		return
	}
	b.FareID = uuid.MustParse(b.FareID).String()
	b.RequestID = uuid.MustParse(b.RequestID).String()
	b.PickupAt = b.PickupAt.UTC().Truncate(time.Microsecond)
	ctx := c.Request.Context()
	u, _ := gatewayauth.CurrentUser(c)
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	var active bool
	if tx.QueryRow(ctx, `SELECT active AND NOT rider_trip_restricted FROM users WHERE id=$1::uuid FOR UPDATE`, u.ID).Scan(&active) != nil || !active {
		driverError(c, 403, "rider_restricted", "Scheduled bookings are not available for this account.")
		return
	}
	var existing, oldFare string
	var oldTime time.Time
	e = tx.QueryRow(ctx, `SELECT id::text,fare_id::text,pickup_at FROM scheduled_rides WHERE rider_id=$1::uuid AND request_id=$2::uuid`, u.ID, b.RequestID).Scan(&existing, &oldFare, &oldTime)
	if e == nil {
		if oldFare != b.FareID || !oldTime.Equal(b.PickupAt) {
			driverError(c, 409, "schedule_conflict", "Request ID was used with different booking details.")
			return
		}
		heroData(c, gin.H{"bookingId": existing})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		heroFailure(c)
		return
	}
	if e := rideridentity.Require(ctx, tx, u.ID); e != nil {
		if errors.Is(e, rideridentity.ErrContactVerificationRequired) {
			driverError(c, 403, "rider_contact_verification_required", "Verify both your email and phone before booking a ride.")
		} else {
			heroFailure(c)
		}
		return
	}
	p, e := readSchedulePolicy(ctx, tx)
	now := time.Now().UTC()
	if e != nil || !p.Enabled || b.PickupAt.Before(now.Add(time.Duration(p.MinAdvance)*time.Minute)) || b.PickupAt.After(now.Add(time.Duration(p.MaxAdvance)*time.Minute)) {
		driverError(c, 409, "schedule_window_unavailable", "Scheduling is disabled or the pickup is outside the supported advance-booking window.")
		return
	}
	var market, region, destRegion string
	var seconds float64
	e = tx.QueryRow(ctx, `SELECT f.pricing_market_code,
 (SELECT CASE WHEN count(*)=1 THEN min(code) ELSE NULL END FROM trip_geofences g WHERE g.kind='region' AND g.status='approved' AND g.effective_from<=NOW() AND (g.effective_until IS NULL OR g.effective_until>NOW()) AND ST_Covers(g.boundary,f.pickup::geometry)),
 (SELECT CASE WHEN count(*)=1 THEN min(code) ELSE NULL END FROM trip_geofences g WHERE g.kind='region' AND g.status='approved' AND g.effective_from<=NOW() AND (g.effective_until IS NULL OR g.effective_until>NOW()) AND ST_Covers(g.boundary,f.destination::geometry)),
 (f.route->'routes'->0->>'duration')::double precision
 FROM ride_fares f JOIN pricing_versions pv ON pv.id=f.pricing_version_id JOIN ride_categories cat ON cat.slug=f.package_slug
 WHERE f.id=$1::uuid AND f.rider_id=$2::uuid AND f.expires_at>NOW() AND f.consumed_at IS NULL AND NOT EXISTS(SELECT 1 FROM rider_offer_quotes oq WHERE oq.fare_id=f.id AND (oq.discount_offer_id IS NOT NULL OR oq.coin_offer_id IS NOT NULL)) AND cat.status='active'
 AND f.package_slug=ANY($3::text[]) AND f.pricing_market_code=ANY($4::text[])
 AND pv.status='approved' AND pv.effective_from<=$5 AND (pv.effective_until IS NULL OR pv.effective_until>$5)
 AND driver_market_at(f.pickup)=f.pricing_market_code AND driver_market_at(f.destination)=f.pricing_market_code
 AND NOT EXISTS(SELECT 1 FROM rider_restrictions WHERE rider_id=$2::uuid AND status='active')
 AND NOT EXISTS(SELECT 1 FROM simulation_participants WHERE actor_user_id=$2::uuid) FOR UPDATE OF f`, b.FareID, u.ID, p.Categories, p.Markets, b.PickupAt).Scan(&market, &region, &destRegion, &seconds)
	if errors.Is(e, pgx.ErrNoRows) {
		driverError(c, 409, "fare_requote_required", "Get a fresh fare for the selected pickup time.")
		return
	}
	if e != nil || seconds <= 0 || seconds > 86400 {
		scheduleFail(c, e)
		return
	}
	id := uuid.NewString()
	e = func() error {
		_, e := tx.Exec(ctx, `INSERT INTO scheduled_rides(id,rider_id,request_id,fare_id,policy_version,pickup_at,dispatch_at,readiness_at,ends_at,market_code,origin_region_code,destination_region_code,free_cancellation_until,pickup_window_end,rider_reminder_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, u.ID, b.RequestID, b.FareID, p.Version, b.PickupAt, b.PickupAt.Add(-time.Duration(p.Dispatch)*time.Minute), b.PickupAt.Add(-time.Duration(p.Readiness)*time.Minute), b.PickupAt.Add(time.Duration(seconds)*time.Second+30*time.Minute), market, region, destRegion, b.PickupAt.Add(-time.Duration(p.FreeCancellation)*time.Minute), b.PickupAt.Add(time.Duration(p.PickupWindow)*time.Minute), b.PickupAt.Add(-time.Duration(p.RiderReminder)*time.Minute))
		return e
	}()
	if e == nil {
		_, e = tx.Exec(ctx, `UPDATE ride_fares SET consumed_at=NOW(),expires_at=$2 WHERE id=$1::uuid`, b.FareID, b.PickupAt.Add(10*time.Minute))
	}
	if e == nil {
		e = scheduleEvent(ctx, tx, id, "scheduled.event.created")
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		scheduleFail(c, e)
		return
	}
	heroData(c, gin.H{"bookingId": id, "status": "open", "farePolicy": "locked_quote", "pickupAt": b.PickupAt, "availabilityGuaranteed": false})
}
func (a *scheduledAPI) list(c *gin.Context, driver bool) {
	u, _ := gatewayauth.CurrentUser(c)
	detail := c.Param("bookingID")
	if detail != "" && uuid.Validate(detail) != nil {
		driverError(c, 400, "invalid_booking", "A booking UUID is required.")
		return
	}
	mode := c.Query("view")
	if mode != "" && mode != "available" && mode != "mine" {
		driverError(c, 400, "invalid_view", "Use available or mine.")
		return
	}
	where := `s.rider_id=$1::uuid`
	if driver {
		where = `s.driver_id=$1::uuid`
		if mode != "mine" {
			where = `s.status='open' AND s.dispatch_at>NOW() AND scheduled_offer_eligible($1::uuid,s.id) AND NOT EXISTS(SELECT 1 FROM scheduled_ride_declines x WHERE x.booking_id=s.id AND x.driver_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM scheduled_ride_amendments a WHERE a.booking_id=s.id AND a.new_fare_id=s.fare_id)) AND NOT EXISTS(SELECT 1 FROM scheduled_ride_amendment_declines x JOIN scheduled_ride_amendments a ON a.booking_id=x.booking_id AND a.request_id=x.request_id WHERE x.booking_id=s.id AND x.driver_id=$1::uuid AND a.new_fare_id=s.fare_id) AND NOT EXISTS(SELECT 1 FROM scheduled_rides own WHERE own.driver_id=$1::uuid AND own.status IN('reserved','dispatched') AND tstzrange(own.readiness_at,own.ends_at,'[)')&&tstzrange(s.readiness_at,s.ends_at,'[)'))`
		}
	}
	if driver && detail != "" {
		where = `(s.driver_id=$1::uuid OR (s.status='open' AND s.dispatch_at>NOW() AND scheduled_offer_eligible($1::uuid,s.id)))`
	}
	if detail != "" {
		where += ` AND s.id=$4::uuid`
	}
	var cursorTime time.Time
	cursorID := "00000000-0000-0000-0000-000000000000"
	if c.Query("after") != "" {
		var e error
		cursorTime, e = time.Parse(time.RFC3339Nano, c.Query("after"))
		cursorID = c.Query("afterId")
		if e != nil || uuid.Validate(cursorID) != nil {
			driverError(c, 400, "invalid_cursor", "Use both returned cursor fields.")
			return
		}
	}
	if cursorTime.IsZero() {
		cursorTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	args := []any{u.ID, cursorTime, cursorID}
	if detail != "" {
		args = append(args, detail)
	}
	rows, e := a.pool.Query(c.Request.Context(), `SELECT s.pickup_at,s.id::text,jsonb_build_object('bookingId',s.id,'serviceType',s.service_type,'status',s.status,'version',s.version,'pickupAt',s.pickup_at,'readinessAt',s.readiness_at,'dispatchAt',s.dispatch_at,'estimatedEndAt',s.ends_at,'readyAt',s.ready_at,'tripId',s.trip_id,'driverId',s.driver_id,'market',s.market_code,'pickupArea',s.origin_region_code,'destinationArea',s.destination_region_code,'packageSlug',f.package_slug,'displayName',cat.name,'passengers',cat.passengers,'fareKobo',ROUND(f.total_fare_minor)::bigint,'currency','NGN','farePolicy','locked_quote','availabilityGuaranteed',false,
 'pickup',CASE WHEN s.rider_id=$1::uuid OR s.driver_id=$1::uuid THEN f.pickup_metadata ELSE NULL END,
 'destination',CASE WHEN s.rider_id=$1::uuid OR s.driver_id=$1::uuid THEN f.destination_metadata ELSE NULL END,
 'noShowDebtKobo',f.no_show_debt_kobo,'amountDueKobo',ROUND(f.total_fare_minor)::bigint+f.no_show_debt_kobo,'reason',s.reason,'cancellationActor',s.cancellation_actor,
 'pickupCoordinate',CASE WHEN s.rider_id=$1::uuid OR s.driver_id=$1::uuid THEN jsonb_build_object('latitude',ST_Y(f.pickup::geometry),'longitude',ST_X(f.pickup::geometry)) ELSE NULL END,
 'destinationCoordinate',CASE WHEN s.rider_id=$1::uuid OR s.driver_id=$1::uuid THEN jsonb_build_object('latitude',ST_Y(f.destination::geometry),'longitude',ST_X(f.destination::geometry)) ELSE NULL END,
 'freeCancellationUntil',s.free_cancellation_until,'pickupWindowStart',s.pickup_at,'pickupWindowEnd',s.pickup_window_end,'paymentMethod',s.payment_method,
 'cancellationReasonCode',s.cancellation_reason_code,'cancellationReasonNote',s.cancellation_reason_note,
 'reminderEnabled',s.rider_reminder_enabled,'reminderAt',s.rider_reminder_at,'reminderSentAt',s.rider_reminder_sent_at,
 'route',CASE WHEN s.rider_id=$1::uuid OR s.driver_id=$1::uuid THEN f.route ELSE NULL END,
 'ridePreferences',CASE WHEN s.rider_id=$1::uuid OR s.driver_id=$1::uuid THEN f.ride_preferences ELSE NULL END,
 'guestName',CASE WHEN s.rider_id=$1::uuid OR (s.driver_id=$1::uuid AND s.status IN ('reserved','dispatched')) THEN f.guest_name ELSE NULL END,
 'guestPhone',CASE WHEN s.rider_id=$1::uuid THEN f.guest_phone ELSE NULL END,
 'pickupNote',CASE WHEN s.rider_id=$1::uuid OR s.driver_id=$1::uuid THEN f.pickup_note ELSE NULL END)
 FROM scheduled_rides s JOIN ride_fares f ON f.id=s.fare_id JOIN ride_categories cat ON cat.slug=f.package_slug WHERE (`+where+`) AND (s.pickup_at,s.id)>($2,$3::uuid) ORDER BY s.pickup_at,s.id LIMIT 51`, args...)
	if e != nil {
		heroFailure(c)
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	type cursor struct {
		At time.Time
		ID string
	}
	var last cursor
	hasMore := false
	for rows.Next() {
		var raw []byte
		var v cursor
		if rows.Scan(&v.At, &v.ID, &raw) != nil {
			heroFailure(c)
			return
		}
		if len(items) == 50 {
			hasMore = true
			break
		}
		last = v
		items = append(items, json.RawMessage(raw))
	}
	if rows.Err() != nil {
		heroFailure(c)
		return
	}
	var next any
	if hasMore {
		next = gin.H{"after": last.At.Format(time.RFC3339Nano), "afterId": last.ID}
	}
	c.Header("Cache-Control", "no-store")
	if detail != "" {
		if len(items) == 0 {
			scheduleFail(c, pgx.ErrNoRows)
			return
		}
		heroData(c, items[0])
		return
	}
	heroData(c, gin.H{"items": items, "nextCursor": next})
}
func (a *scheduledAPI) accept(c *gin.Context)  { a.transition(c, "accept") }
func (a *scheduledAPI) decline(c *gin.Context) { a.transition(c, "decline") }
func (a *scheduledAPI) release(c *gin.Context) { a.transition(c, "release") }
func (a *scheduledAPI) cancel(c *gin.Context)  { a.transition(c, "cancel") }
func (a *scheduledAPI) transition(c *gin.Context, action string) {
	var cancelReason scheduledCancelReason
	if action == "cancel" && (c.ShouldBindJSON(&cancelReason) != nil || !validScheduledCancelReason(cancelReason)) {
		driverError(c, 400, "invalid_cancellation_reason", "Choose a booking cancellation reason; notes must be at most 500 characters.")
		return
	}
	id := c.Param("bookingID")
	if uuid.Validate(id) != nil {
		driverError(c, 400, "invalid_booking", "A booking UUID is required.")
		return
	}
	ctx := c.Request.Context()
	u, _ := gatewayauth.CurrentUser(c)
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	// Driver lock serializes acceptance with ordinary matching and other accepts.
	if _, e = tx.Exec(ctx, `SELECT u.id FROM users u JOIN scheduled_rides s ON s.rider_id=u.id WHERE s.id=$1::uuid FOR SHARE OF u`, id); e != nil {
		heroFailure(c)
		return
	}
	if action != "cancel" {
		var lock string
		if e = tx.QueryRow(ctx, `SELECT id::text FROM drivers WHERE id=$1::uuid FOR UPDATE`, u.ID).Scan(&lock); e != nil {
			scheduleFail(c, e)
			return
		}
	}
	var owner, state string
	var assigned *string
	var dispatch, cancelUntil time.Time
	e = tx.QueryRow(ctx, `SELECT rider_id::text,status,driver_id::text,dispatch_at,free_cancellation_until FROM scheduled_rides WHERE id=$1::uuid FOR UPDATE`, id).Scan(&owner, &state, &assigned, &dispatch, &cancelUntil)
	if e != nil {
		scheduleFail(c, e)
		return
	}
	if action == "cancel" && owner != u.ID || action == "release" && (assigned == nil || *assigned != u.ID) {
		scheduleFail(c, pgx.ErrNoRows)
		return
	}
	if action == "accept" && state == "reserved" && assigned != nil && *assigned == u.ID {
		heroData(c, gin.H{"bookingId": id, "status": state})
		return
	}
	if action == "cancel" && state == "cancelled" {
		heroData(c, gin.H{"bookingId": id, "status": state})
		return
	}
	if (!dispatch.After(time.Now()) && (action == "accept" || action == "decline")) || state != "open" && state != "reserved" {
		scheduleFail(c, errors.New("state changed"))
		return
	}
	if action == "accept" || action == "decline" {
		var eligible bool
		if e = tx.QueryRow(ctx, `SELECT scheduled_offer_eligible($1::uuid,$2::uuid)`, u.ID, id).Scan(&eligible); e != nil || !eligible || state != "open" {
			scheduleFail(c, e)
			return
		}
	}
	switch action {
	case "accept":
		var busy bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trips WHERE assigned_driver_id=$1::uuid AND status IN('assigned','accepted','arrived','started') AND NOW()+make_interval(secs=>COALESCE((SELECT (f.route->'routes'->0->>'duration')::double precision FROM ride_fares f WHERE f.id=trips.ride_fare_id),86400))+INTERVAL '15 minutes'>=(SELECT readiness_at FROM scheduled_rides WHERE id=$2::uuid))`, u.ID, id).Scan(&busy)
		if e != nil || busy {
			scheduleFail(c, e)
			return
		}
		_, e = tx.Exec(ctx, `UPDATE scheduled_rides SET status='reserved',driver_id=$2::uuid,version=version+1,updated_at=NOW() WHERE id=$1::uuid`, id, u.ID)
	case "decline":
		_, e = tx.Exec(ctx, `INSERT INTO scheduled_ride_declines(booking_id,driver_id)VALUES($1::uuid,$2::uuid)ON CONFLICT DO NOTHING`, id, u.ID)
		if e == nil {
			_, e = tx.Exec(ctx, `INSERT INTO scheduled_ride_amendment_declines(booking_id,request_id,driver_id) SELECT a.booking_id,a.request_id,$2::uuid FROM scheduled_ride_amendments a JOIN scheduled_rides s ON s.id=a.booking_id AND s.fare_id=a.new_fare_id WHERE s.id=$1::uuid ON CONFLICT DO NOTHING`, id, u.ID)
		}
	case "release":
		_, e = tx.Exec(ctx, `UPDATE scheduled_rides SET status='open',driver_id=NULL,ready_at=NULL,reminder_sent_at=NULL,version=version+1,updated_at=NOW() WHERE id=$1::uuid`, id)
	case "cancel":
		if !cancelUntil.After(time.Now()) {
			driverError(c, 409, "scheduled_cancellation_closed", "The booking cancellation deadline has passed. Contact support or open the dispatched trip.")
			return
		}
		_, e = tx.Exec(ctx, `UPDATE scheduled_rides SET status='cancelled',cancellation_actor='rider',reason=$2,cancellation_reason_code=$2,cancellation_reason_note=$3,version=version+1,updated_at=NOW() WHERE id=$1::uuid`, id, cancelReason.Code, strings.TrimSpace(cancelReason.Note))
	}
	if e == nil && action != "decline" {
		extra := []string{}
		if action == "release" {
			extra = append(extra, u.ID)
		}
		e = scheduleEvent(ctx, tx, id, "scheduled.event."+action, extra...)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		scheduleFail(c, e)
		return
	}
	heroData(c, gin.H{"bookingId": id, "action": action, "refreshScheduledRides": true})
}
func (a *scheduledAPI) ready(c *gin.Context) {
	id := c.Param("bookingID")
	if uuid.Validate(id) != nil {
		driverError(c, 400, "invalid_booking", "A booking UUID is required.")
		return
	}
	ctx := c.Request.Context()
	u, _ := gatewayauth.CurrentUser(c)
	var origin, pickup geo.Coordinate
	var at time.Time
	e := a.pool.QueryRow(ctx, `SELECT ST_Y(l.location::geometry),ST_X(l.location::geometry),ST_Y(f.pickup::geometry),ST_X(f.pickup::geometry),s.pickup_at FROM scheduled_rides s JOIN ride_fares f ON f.id=s.fare_id JOIN drivers d ON d.id=s.driver_id JOIN driver_live_locations l ON l.driver_id=d.id JOIN driver_eligibility e ON e.driver_id=d.id WHERE s.id=$1::uuid AND s.driver_id=$2::uuid AND s.status='reserved' AND NOW()>=s.readiness_at AND NOW()<s.dispatch_at AND scheduled_offer_eligible(d.id,s.id) AND d.online_requested AND d.status='available' AND d.available AND e.eligible_to_drive AND l.recorded_at>=NOW()-INTERVAL '2 minutes' AND l.recorded_at<=NOW()+INTERVAL '10 seconds' AND EXISTS(SELECT 1 FROM driver_socket_sessions ds WHERE ds.driver_id=d.id AND ds.expires_at>NOW())`, id, u.ID).Scan(&origin.Latitude, &origin.Longitude, &pickup.Latitude, &pickup.Longitude, &at)
	if e != nil {
		scheduleFail(c, e)
		return
	}
	routeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	route, e := a.router.Route(routeCtx, origin, pickup)
	if e != nil || route == nil || len(route.Routes) == 0 || route.Routes[0].Duration <= 0 || time.Now().Add(time.Duration(route.Routes[0].Duration)*time.Second+5*time.Minute).After(at) {
		driverError(c, 409, "cannot_reach_pickup", "A fresh road-route estimate must allow arrival before pickup. Go online nearby and try again.")
		return
	}
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT u.id FROM users u JOIN scheduled_rides s ON s.rider_id=u.id WHERE s.id=$1::uuid FOR SHARE OF u`, id); e != nil {
		heroFailure(c)
		return
	}
	tag, e := tx.Exec(ctx, `UPDATE scheduled_rides SET ready_at=NOW(),version=version+1,updated_at=NOW() WHERE id=$1::uuid AND driver_id=$2::uuid AND status='reserved' AND dispatch_at>NOW() AND ready_at IS NULL AND scheduled_offer_eligible($2::uuid,id) AND EXISTS(SELECT 1 FROM drivers d JOIN driver_live_locations l ON l.driver_id=d.id WHERE d.id=$2::uuid AND d.status='available' AND d.available AND d.online_requested AND l.recorded_at>=NOW()-INTERVAL '2 minutes' AND ST_DWithin(l.location,ST_SetSRID(ST_MakePoint($3,$4),4326)::geography,100))`, id, u.ID, origin.Longitude, origin.Latitude)
	if e == nil && tag.RowsAffected() == 0 {
		var already bool
		if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM scheduled_rides WHERE id=$1::uuid AND driver_id=$2::uuid AND status='reserved' AND dispatch_at>NOW() AND ready_at IS NOT NULL)`, id, u.ID).Scan(&already) == nil && already {
			heroData(c, gin.H{"bookingId": id, "ready": true})
			return
		}
		e = pgx.ErrNoRows
	}
	if e == nil {
		e = scheduleEvent(ctx, tx, id, "scheduled.event.ready")
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		scheduleFail(c, e)
		return
	}
	heroData(c, gin.H{"bookingId": id, "ready": true, "pickupEtaSeconds": route.Routes[0].Duration})
}
