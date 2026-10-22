package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func (a *driverAPI) driverNotificationPreferences(c *gin.Context) {
	var ride, trip, earnings, documents, announcements, promotions bool
	err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE(p.ride_requests,TRUE),COALESCE(p.trip_updates,TRUE),COALESCE(p.earnings,TRUE),COALESCE(p.document_reminders,TRUE),COALESCE(p.announcements,TRUE),COALESCE(p.promotions,FALSE) FROM users u LEFT JOIN driver_notification_preferences p ON p.driver_id=u.id WHERE u.id=$1::UUID`, driverID(c)).Scan(&ride, &trip, &earnings, &documents, &announcements, &promotions)
	if err != nil {
		driverError(c, 503, "preferences_unavailable", "Notification preferences are unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"rideRequests": ride, "tripUpdates": trip, "earnings": earnings, "documentReminders": documents, "announcements": announcements, "promotions": promotions, "scope": "push_only", "inboxAlwaysRetained": true}})
}
func (a *driverAPI) patchDriverNotificationPreferences(c *gin.Context) {
	var p struct {
		Ride          *bool `json:"rideRequests"`
		Trip          *bool `json:"tripUpdates"`
		Earnings      *bool `json:"earnings"`
		Documents     *bool `json:"documentReminders"`
		Announcements *bool `json:"announcements"`
		Promotions    *bool `json:"promotions"`
	}
	if c.ShouldBindJSON(&p) != nil || (p.Ride == nil && p.Trip == nil && p.Earnings == nil && p.Documents == nil && p.Announcements == nil && p.Promotions == nil) {
		driverError(c, 400, "invalid_preferences", "At least one notification preference is required")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "preferences_unavailable", "Preferences could not be saved")
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1::UUID::TEXT,0))`, driverID(c)); err != nil {
		driverError(c, 503, "preferences_unavailable", "Preferences could not be saved")
		return
	}
	var owner string
	err = tx.QueryRow(c.Request.Context(), `SELECT id::TEXT FROM users WHERE id=$1::UUID FOR UPDATE`, driverID(c)).Scan(&owner)
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO driver_notification_preferences(driver_id,ride_requests,trip_updates,earnings,document_reminders,announcements,promotions) VALUES($1::UUID,COALESCE($2,TRUE),COALESCE($3,TRUE),COALESCE($4,TRUE),COALESCE($5,TRUE),COALESCE($6,TRUE),COALESCE($7,FALSE)) ON CONFLICT(driver_id) DO UPDATE SET ride_requests=COALESCE($2,driver_notification_preferences.ride_requests),trip_updates=COALESCE($3,driver_notification_preferences.trip_updates),earnings=COALESCE($4,driver_notification_preferences.earnings),document_reminders=COALESCE($5,driver_notification_preferences.document_reminders),announcements=COALESCE($6,driver_notification_preferences.announcements),promotions=COALESCE($7,driver_notification_preferences.promotions),updated_at=NOW()`, owner, p.Ride, p.Trip, p.Earnings, p.Documents, p.Announcements, p.Promotions)
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE driver_notification_push_deliveries d SET status='dead',lease_until=NULL,last_error='preference_disabled' FROM driver_notifications n WHERE n.id=d.notification_id AND n.driver_id=$1::UUID AND d.status IN ('pending','processing') AND NOT driver_notification_allowed(n.driver_id,n.type)`, owner)
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		driverError(c, 503, "preferences_unavailable", "Preferences could not be saved")
		return
	}
	a.driverNotificationPreferences(c)
}
