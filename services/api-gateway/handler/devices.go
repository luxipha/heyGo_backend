package handler

import (
	"net/http"
	"strings"

	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
	err = pool.QueryRow(ctx.Request.Context(), `INSERT INTO driver_devices(id,driver_id,platform,push_token,app_version)
		VALUES($1,$2::UUID,$3,$4,$5)
		ON CONFLICT(driver_id,push_token) DO UPDATE SET platform=EXCLUDED.platform,app_version=EXCLUDED.app_version,updated_at=NOW()
		RETURNING id`, id, user.ID, body.Platform, strings.TrimSpace(body.PushToken), strings.TrimSpace(body.AppVersion)).Scan(&id)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "device_unavailable", "Device registration is unavailable")
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
