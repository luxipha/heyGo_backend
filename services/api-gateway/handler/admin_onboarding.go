package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/driverstate"
)

// Caller data intentionally excludes document contents, identity numbers,
// balances, trips, internal verification notes and mutation endpoints.
type onboardingDriver struct {
	DriverID    string    `json:"driverId"`
	DisplayName string    `json:"displayName"`
	Phone       string    `json:"phone"`
	SignedUpAt  time.Time `json:"signedUpAt"`
	AdminStatus string    `json:"adminStatus"`
}

const onboardingSource = ` FROM users u
 LEFT JOIN driver_profiles p ON p.driver_id=u.id
 LEFT JOIN driver_identity_profiles ip ON ip.driver_id=u.id `
const onboardingColumns = `u.id::TEXT,COALESCE(NULLIF(BTRIM(CONCAT_WS(' ',ip.first_name,ip.last_name)),''),NULLIF(BTRIM(p.display_name),''),''),COALESCE(u.phone_e164,''),u.created_at,COALESCE(p.admin_status,'pending')`

func (a *adminAPI) listOnboardingDrivers(c *gin.Context) {
	status := c.DefaultQuery("status", "incomplete")
	if status != "incomplete" && status != "completed" && status != "all" {
		driverError(c, 400, "invalid_status", "Status must be incomplete, completed or all")
		return
	}
	search := strings.TrimSpace(c.Query("q"))
	if len(search) > 100 {
		driverError(c, 400, "invalid_search", "Search is too long")
		return
	}
	limit, err := parseDriverTripHistoryLimit(c.DefaultQuery("limit", "25"))
	if err != nil {
		driverError(c, 400, "invalid_limit", err.Error())
		return
	}
	var cursorTime, cursorID any
	if raw := c.Query("cursor"); raw != "" {
		cursor, err := decodeDriverTripHistoryCursor(raw)
		if err != nil {
			driverError(c, 400, "invalid_cursor", "Driver cursor is invalid")
			return
		}
		cursorTime, cursorID = cursor.OccurredAt, cursor.ID
	}
	rows, err := a.pool.Query(c.Request.Context(), `SELECT `+onboardingColumns+onboardingSource+`
 WHERE 'driver'=ANY(u.roles)
 AND ($1='all' OR ($1='completed' AND p.admin_status='approved') OR ($1='incomplete' AND COALESCE(p.admin_status,'pending')<>'approved'))
 AND ($2='' OR STRPOS(LOWER(CONCAT_WS(' ',ip.first_name,ip.last_name,p.display_name,u.phone_e164)),LOWER($2))>0)
 AND ($3::TIMESTAMPTZ IS NULL OR (u.created_at,u.id)<($3,$4::UUID))
 ORDER BY u.created_at DESC,u.id DESC LIMIT $5`, status, search, cursorTime, cursorID, limit+1)
	if err != nil {
		reviewFailure(c)
		return
	}
	items := make([]onboardingDriver, 0, limit+1)
	for rows.Next() {
		var d onboardingDriver
		if err := rows.Scan(&d.DriverID, &d.DisplayName, &d.Phone, &d.SignedUpAt, &d.AdminStatus); err != nil {
			rows.Close()
			reviewFailure(c)
			return
		}
		items = append(items, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		reviewFailure(c)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		value := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: last.SignedUpAt, ID: last.DriverID})
		next = &value
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) onboardingSummary(c *gin.Context) {
	var total, completed int
	err := a.pool.QueryRow(c.Request.Context(), `SELECT COUNT(*),COUNT(*) FILTER(WHERE p.admin_status='approved') FROM users u LEFT JOIN driver_profiles p ON p.driver_id=u.id WHERE 'driver'=ANY(u.roles)`).Scan(&total, &completed)
	if err != nil {
		reviewFailure(c)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"total": total, "completed": completed}})
}

func (a *adminAPI) onboardingDriver(c *gin.Context) {
	id, ok := reviewDriverID(c)
	if !ok {
		return
	}
	var d onboardingDriver
	err := a.pool.QueryRow(c.Request.Context(), `SELECT `+onboardingColumns+onboardingSource+` WHERE u.id=$1::UUID AND 'driver'=ANY(u.roles)`, id).Scan(&d.DriverID, &d.DisplayName, &d.Phone, &d.SignedUpAt, &d.AdminStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "driver_not_found", "Driver was not found")
		return
	}
	if err != nil {
		reviewFailure(c)
		return
	}
	progress, err := driverstate.Read(c.Request.Context(), a.pool, id)
	if err != nil {
		reviewFailure(c)
		return
	}
	var identityComplete, vehicleComplete bool
	err = a.pool.QueryRow(c.Request.Context(), `SELECT
 EXISTS(SELECT 1 FROM driver_identity_profiles WHERE driver_id=$1::UUID AND BTRIM(first_name)<>'' AND BTRIM(last_name)<>'' AND date_of_birth IS NOT NULL AND BTRIM(residential_address)<>'' AND nin_last4<>''),
 EXISTS(SELECT 1 FROM driver_vehicles WHERE driver_id=$1::UUID AND BTRIM(make)<>'' AND BTRIM(model)<>'' AND BTRIM(plate)<>'' AND model_year>=1900)`, id).Scan(&identityComplete, &vehicleComplete)
	if err != nil {
		reviewFailure(c)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"driver": d, "requirements": progress.Requirements, "driverDetailsComplete": identityComplete, "vehicleDetailsComplete": vehicleComplete}})
}
