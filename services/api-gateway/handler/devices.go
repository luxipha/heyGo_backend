package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func registerDeviceRoutes(authenticated *gin.RouterGroup, pool *pgxpool.Pool) {
	authenticated.POST("/devices", gatewayauth.RequireRole("driver"), func(ctx *gin.Context) { registerDevice(ctx, pool) })
	authenticated.DELETE("/devices/:deviceID", gatewayauth.RequireRole("driver"), func(ctx *gin.Context) { removeDevice(ctx, pool) })
}

func registerDevice(ctx *gin.Context, pool *pgxpool.Pool) {
	var body struct {
		ID         string `json:"id"`
		Platform   string `json:"platform"`
		PushToken  string `json:"pushToken"`
		AppVersion string `json:"appVersion"`
	}
	if err := ctx.ShouldBindJSON(&body); err != nil || body.ID == "" || len(body.ID) > 128 || (body.Platform != "ios" && body.Platform != "android") || strings.TrimSpace(body.PushToken) == "" || len(body.PushToken) > 4096 || len(body.AppVersion) > 64 {
		driverError(ctx, http.StatusBadRequest, "invalid_device", "Device ID, platform, and push token are required")
		return
	}
	id, err := uuid.Parse(body.ID)
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_device_id", "Device ID must be a UUID")
		return
	}
	user, _ := gatewayauth.CurrentUser(ctx)
	tx, err := pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, 503, "device_unavailable", "Device registration is unavailable")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	// Serialize claims of a token so a physical app token has only one Driver owner.
	if _, err = tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, strings.TrimSpace(body.PushToken)); err != nil {
		driverError(ctx, 503, "device_unavailable", "Device registration is unavailable")
		return
	}
	var sessionID any
	var linked string
	authenticatedSession := gatewayauth.CurrentSessionID(ctx)
	var sessionArgument any
	if authenticatedSession != "" {
		sessionArgument = authenticatedSession
	}
	err = tx.QueryRow(ctx.Request.Context(), `SELECT id::text FROM user_sessions WHERE user_id=$1::uuid AND revoked_at IS NULL AND (($3::uuid IS NOT NULL AND id=$3::uuid) OR ($3::uuid IS NULL AND access_token_hash=$2)) FOR UPDATE`, user.ID, gatewayauth.TokenHash(bearerFromHeader(ctx.GetHeader("Authorization"))), sessionArgument).Scan(&linked)
	if err == nil {
		sessionID = linked
	} else if err == pgx.ErrNoRows && authenticatedSession == "" {
	} else if err == pgx.ErrNoRows {
		driverError(ctx, 401, "session_expired", "Session expired")
		return
	} else {
		driverError(ctx, 503, "device_unavailable", "Device registration is unavailable")
		return
	}
	var foreign bool
	if err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_devices WHERE id=$1 AND driver_id<>$2::uuid)`, id, user.ID).Scan(&foreign); err != nil {
		driverError(ctx, 503, "device_unavailable", "Device registration is unavailable")
		return
	}
	if foreign {
		driverError(ctx, 409, "device_conflict", "Device belongs to another account")
		return
	}
	if err = tx.QueryRow(ctx.Request.Context(), `SELECT COALESCE((SELECT id FROM driver_devices WHERE driver_id=$1::uuid AND push_token=$2),$3::uuid)`, user.ID, strings.TrimSpace(body.PushToken), id).Scan(&id); err != nil {
		driverError(ctx, 503, "device_unavailable", "Device registration is unavailable")
		return
	}
	if _, err = tx.Exec(ctx.Request.Context(), `DELETE FROM driver_devices WHERE push_token=$1 AND id<>$2`, strings.TrimSpace(body.PushToken), id); err != nil {
		driverError(ctx, 503, "device_unavailable", "Device registration is unavailable")
		return
	}
	err = tx.QueryRow(ctx.Request.Context(), `INSERT INTO driver_devices(id,driver_id,platform,push_token,app_version,session_id)
 VALUES($1,$2::uuid,$3,$4,$5,$6::uuid) ON CONFLICT(id) DO UPDATE SET platform=EXCLUDED.platform,push_token=EXCLUDED.push_token,app_version=EXCLUDED.app_version,session_id=EXCLUDED.session_id,revoked_at=NULL,updated_at=NOW() WHERE driver_devices.driver_id=EXCLUDED.driver_id RETURNING id`, id, user.ID, body.Platform, strings.TrimSpace(body.PushToken), strings.TrimSpace(body.AppVersion), sessionID).Scan(&id)
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		driverError(ctx, 503, "device_unavailable", "Device registration is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id.String(), "platform": body.Platform}})
}

func removeDevice(ctx *gin.Context, pool *pgxpool.Pool) {
	id, err := uuid.Parse(ctx.Param("deviceID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_device_id", "Device ID is invalid")
		return
	}
	user, _ := gatewayauth.CurrentUser(ctx)
	if _, err := pool.Exec(ctx.Request.Context(), `DELETE FROM driver_devices WHERE id=$1 AND driver_id=$2::UUID`, id, user.ID); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "device_unavailable", "Device removal is unavailable")
		return
	}
	ctx.Status(http.StatusNoContent)
}
