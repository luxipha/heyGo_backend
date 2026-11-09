package handler

import "github.com/gin-gonic/gin"

func validSchedulePolicy(p schedulePolicy) bool {
	if p.FreeCancellation < p.Dispatch || p.FreeCancellation > p.MinAdvance || p.PickupWindow < 0 || p.PickupWindow > 30 || p.RiderReminder < 5 || p.RiderReminder > 1440 {
		return false
	}
	return p.Version > 0 && p.MinAdvance >= 60 && p.MinAdvance <= 10080 && p.MaxAdvance >= p.MinAdvance && p.MaxAdvance <= 43200 && p.Dispatch >= 10 && p.Dispatch <= 60 && p.Readiness >= 15 && p.Readiness > p.Dispatch && p.Readiness <= 120 && p.Readiness < p.MinAdvance && len(p.Markets) <= 100 && len(p.Categories) <= 100 && (!p.Enabled || len(p.Markets) > 0 && len(p.Categories) > 0)
}
func (a *adminAPI) getSchedulePolicy(c *gin.Context) {
	p, e := readSchedulePolicy(c.Request.Context(), a.pool)
	if e != nil {
		heroFailure(c)
		return
	}
	heroData(c, p)
}
func (a *adminAPI) saveSchedulePolicy(c *gin.Context) {
	var p schedulePolicy
	if c.ShouldBindJSON(&p) != nil || !validSchedulePolicy(p) {
		driverError(c, 400, "invalid_schedule_policy", "Use enabled markets/categories and ordered readiness, dispatch and advance-booking windows.")
		return
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('scheduled-policy'))`); e != nil {
		heroFailure(c)
		return
	}
	old, e := readSchedulePolicy(ctx, tx)
	if e != nil {
		heroFailure(c)
		return
	}
	if old.Version != p.Version {
		driverError(c, 409, "stale_schedule_policy", "Reload policy before saving.")
		return
	}
	for _, slug := range p.Categories {
		var exists bool
		if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ride_categories WHERE slug=$1 AND status='active')`, slug).Scan(&exists) != nil || !exists {
			driverError(c, 400, "invalid_schedule_category", "Use active catalogue category slugs.")
			return
		}
	}
	for _, market := range p.Markets {
		var exists bool
		if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trip_geofences WHERE kind='market' AND code=$1 AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW()))`, market).Scan(&exists) != nil || !exists {
			driverError(c, 400, "invalid_schedule_market", "Use active market codes.")
			return
		}
	}
	if p.Markets == nil {
		p.Markets = []string{}
	}
	if p.Categories == nil {
		p.Categories = []string{}
	}
	e = tx.QueryRow(ctx, `INSERT INTO scheduled_ride_policies(enabled,market_codes,category_slugs,min_advance_minutes,max_advance_minutes,dispatch_minutes,readiness_minutes,created_by,free_cancellation_minutes,pickup_window_minutes,rider_reminder_minutes)VALUES($1,$2,$3,$4,$5,$6,$7,$8::uuid,$9,$10,$11)RETURNING version`, p.Enabled, p.Markets, p.Categories, p.MinAdvance, p.MaxAdvance, p.Dispatch, p.Readiness, currentAdmin(c).ID, p.FreeCancellation, p.PickupWindow, p.RiderReminder).Scan(&p.Version)
	if e == nil {
		e = adminAudit(c, tx, "scheduling.policy.created", "", p)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		heroFailure(c)
		return
	}
	heroData(c, p)
}
