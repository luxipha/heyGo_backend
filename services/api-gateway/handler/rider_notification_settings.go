package handler

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"strconv"
	"strings"
	"time"
)

func (a *riderAccountAPI) notificationPreferences(c *gin.Context) {
	var ride, account, promotions bool
	var updated *time.Time
	err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE(p.ride_updates,TRUE),COALESCE(p.account_alerts,TRUE),COALESCE(p.promotions,FALSE),p.updated_at FROM users u LEFT JOIN rider_notification_preferences p ON p.rider_id=u.id WHERE u.id=$1::UUID AND u.active`, riderAccountOwner(c)).Scan(&ride, &account, &promotions, &updated)
	if err != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"rideUpdates": ride, "accountAlerts": account, "promotions": promotions, "updatedAt": updated, "scope": "push_only", "inboxAlwaysRetained": true}})
}
func (a *riderAccountAPI) patchNotificationPreferences(c *gin.Context) {
	var p struct {
		Ride       *bool `json:"rideUpdates"`
		Account    *bool `json:"accountAlerts"`
		Promotions *bool `json:"promotions"`
	}
	if c.ShouldBindJSON(&p) != nil || (p.Ride == nil && p.Account == nil && p.Promotions == nil) {
		driverError(c, 400, "invalid_preferences", "At least one notification preference is required")
		return
	}
	tx, err := a.lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO rider_notification_preferences(rider_id,ride_updates,account_alerts,promotions) VALUES($1::UUID,COALESCE($2,TRUE),COALESCE($3,TRUE),COALESCE($4,FALSE)) ON CONFLICT(rider_id) DO UPDATE SET ride_updates=COALESCE($2,rider_notification_preferences.ride_updates),account_alerts=COALESCE($3,rider_notification_preferences.account_alerts),promotions=COALESCE($4,rider_notification_preferences.promotions),updated_at=NOW()`, riderAccountOwner(c), p.Ride, p.Account, p.Promotions)
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE rider_notification_push_deliveries d SET status='dead',lease_until=NULL,last_error='preference_disabled' FROM rider_notifications n JOIN rider_notification_preferences p ON p.rider_id=n.rider_id WHERE d.notification_id=n.id AND n.rider_id=$1::UUID AND d.status='pending' AND NOT CASE n.type WHEN 'ride_updates' THEN p.ride_updates WHEN 'account_alerts' THEN p.account_alerts ELSE p.promotions END`, riderAccountOwner(c))
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		accountError(c)
		return
	}
	a.notificationPreferences(c)
}
func (a *riderAccountAPI) registerRiderDevice(c *gin.Context) {
	var b struct {
		ID       string `json:"id"`
		Platform string `json:"platform"`
		Token    string `json:"pushToken"`
		Version  string `json:"appVersion"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_device", "Device details are required")
		return
	}
	id, err := uuid.Parse(b.ID)
	b.Token = strings.TrimSpace(b.Token)
	if err != nil || (b.Platform != "ios" && b.Platform != "android") || b.Token == "" || len(b.Token) > 4096 || len(b.Version) > 64 {
		driverError(c, 400, "invalid_device", "Valid device ID, platform and push token are required")
		return
	}
	tx, err := a.lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var sessionID string
	err = tx.QueryRow(c.Request.Context(), `SELECT id::TEXT FROM user_sessions WHERE user_id=$1::UUID AND access_token_hash=$2 AND revoked_at IS NULL AND access_expires_at>NOW()`, riderAccountOwner(c), gatewayauth.TokenHash(bearerFromHeader(c.GetHeader("Authorization")))).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 401, "authentication_required", "An active HeyGo session is required for device registration")
		return
	}
	if err != nil {
		accountError(c)
		return
	}
	// Release a token only when its previous session has been revoked/expired.
	if _, err = tx.Exec(c.Request.Context(), `UPDATE rider_devices d SET enabled=FALSE,push_token='',updated_at=NOW() FROM user_sessions s WHERE d.session_id=s.id AND d.push_token=$1 AND d.enabled AND (s.revoked_at IS NOT NULL OR s.refresh_expires_at<=NOW())`, b.Token); err != nil {
		accountError(c)
		return
	}
	result, err := tx.Exec(c.Request.Context(), `INSERT INTO rider_devices(id,rider_id,platform,push_token,app_version,session_id) VALUES($1,$2::UUID,$3,$4,$5,$6::UUID) ON CONFLICT(id) DO UPDATE SET session_id=EXCLUDED.session_id,platform=EXCLUDED.platform,push_token=EXCLUDED.push_token,app_version=EXCLUDED.app_version,enabled=TRUE,updated_at=NOW() WHERE rider_devices.rider_id=EXCLUDED.rider_id`, id, riderAccountOwner(c), b.Platform, b.Token, strings.TrimSpace(b.Version), sessionID)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		driverError(c, 409, "device_conflict", "Device token is already registered; remove the previous registration first")
		return
	}
	if err != nil {
		accountError(c)
		return
	}
	if result.RowsAffected() != 1 {
		driverError(c, 409, "device_conflict", "Device identity belongs to another account")
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": id.String(), "platform": b.Platform, "registered": true}})
}
func (a *riderAccountAPI) removeRiderDevice(c *gin.Context) {
	id, err := uuid.Parse(c.Param("deviceID"))
	if err != nil {
		driverError(c, 400, "invalid_device_id", "Device ID is invalid")
		return
	}
	tx, err := a.lockOwner(c)
	if err != nil {
		accountError(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, err = tx.Exec(c.Request.Context(), `UPDATE rider_devices SET enabled=FALSE,push_token='',updated_at=NOW() WHERE id=$1 AND rider_id=$2::UUID`, id, riderAccountOwner(c))
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE rider_notification_push_deliveries SET status='dead',lease_until=NULL,last_error='device_removed' WHERE device_id IN (SELECT id FROM rider_devices WHERE id=$1 AND rider_id=$2::UUID) AND status IN ('pending','processing')`, id, riderAccountOwner(c))
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		accountError(c)
		return
	}
	c.Status(204)
}
func notificationID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		driverError(c, 400, "invalid_notification_id", "Notification ID is invalid")
		return 0, false
	}
	return id, true
}
func (a *riderAccountAPI) riderNotificationDetail(c *gin.Context) {
	id, ok := notificationID(c)
	if !ok {
		return
	}
	var n riderNotification
	err := a.pool.QueryRow(c.Request.Context(), `SELECT id::TEXT,type,title,message,data,created_at,read_at FROM rider_notifications WHERE id=$1 AND rider_id=$2::UUID`, id, riderAccountOwner(c)).Scan(&n.ID, &n.Type, &n.Title, &n.Message, &n.Data, &n.CreatedAt, &n.ReadAt)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "notification_not_found", "Notification was not found")
		return
	}
	if err != nil {
		accountError(c)
		return
	}
	// The hint is read from owned storage, never trusted directly from an incoming push.
	c.JSON(200, contracts.APIResponse{Data: n})
}
func (a *riderAccountAPI) riderNotificationDeliveries(c *gin.Context) {
	id, ok := notificationID(c)
	if !ok {
		return
	}
	var owned bool
	if a.pool.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM rider_notifications WHERE id=$1 AND rider_id=$2::UUID)`, id, riderAccountOwner(c)).Scan(&owned) != nil {
		accountError(c)
		return
	}
	if !owned {
		driverError(c, 404, "notification_not_found", "Notification was not found")
		return
	}
	var raw json.RawMessage
	err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',d.id::TEXT,'status',d.status,'attempts',d.attempts,'sentAt',d.sent_at,'nextAttemptAt',d.next_attempt_at,'reason',d.last_error) ORDER BY d.id),'[]'::JSONB) FROM rider_notification_push_deliveries d WHERE notification_id=$1`, id).Scan(&raw)
	if err != nil {
		accountError(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"items": raw, "sentMeaning": "provider_accepted_not_device_receipt"}})
}
