package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

const livePollSeconds = 10
const liveStaleAfter = 30 * time.Second
const liveDisconnectedAfter = 60 * time.Second

type staffLiveDriver struct {
	DriverID   string     `json:"driverId"`
	MarketCode *string    `json:"marketCode"`
	Status     string     `json:"status"`
	TripID     *string    `json:"tripId"`
	Latitude   *float64   `json:"latitude,omitempty"`
	Longitude  *float64   `json:"longitude,omitempty"`
	RecordedAt *time.Time `json:"recordedAt,omitempty"`
	Presence   string     `json:"presence"`
}

type staffAttentionItem struct {
	Type            string    `json:"type"`
	EntityID        string    `json:"entityId"`
	Priority        string    `json:"priority"`
	Owner           string    `json:"owner"`
	AssignedAdminID *string   `json:"assignedAdminId"`
	Destination     string    `json:"destination"`
	CreatedAt       time.Time `json:"createdAt"`
}

func livePresence(asOf time.Time, onlineRequested, socketPresent bool, recordedAt *time.Time) string {
	if !onlineRequested {
		return "offline"
	}
	if !socketPresent || recordedAt == nil || asOf.Sub(*recordedAt) >= liveDisconnectedAfter {
		return "disconnected"
	}
	if asOf.Sub(*recordedAt) >= liveStaleAfter {
		return "stale"
	}
	return "current"
}

func registerAdminLiveRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/live/drivers", a.liveDrivers)
	group.GET("/live/markers", a.liveMarkers)
	group.GET("/overview", a.overview)
}

func (a *adminAPI) liveDrivers(ctx *gin.Context) {
	limit := 100
	if raw := ctx.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			driverError(ctx, http.StatusBadRequest, "invalid_limit", "Limit must be 1 to 100")
			return
		}
		limit = value
	}
	var cursor any
	if raw := ctx.Query("cursor"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Valid Driver cursor is required")
			return
		}
		cursor = id
	}
	var driverID any
	if raw := ctx.Query("driverId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_driver_id", "Valid Driver ID is required")
			return
		}
		driverID = id
	}
	asOf := time.Now().UTC()
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT d.id::TEXT,d.online_market_code,d.status,d.online_requested,
        EXISTS(SELECT 1 FROM driver_socket_sessions s WHERE s.driver_id=d.id AND s.expires_at>$1),
        ST_Y(l.location::geometry),ST_X(l.location::geometry),l.recorded_at,
        (SELECT t.id::TEXT FROM trips t WHERE t.assigned_driver_id=d.id AND t.status IN ('assigned','accepted','arrived','started') ORDER BY t.updated_at DESC LIMIT 1)
        FROM drivers d LEFT JOIN driver_live_locations l ON l.driver_id=d.id
		WHERE (d.online_requested OR d.status IN ('offered','on_trip'))
		AND ($2::UUID IS NULL OR d.id=$2)
		AND ($3::UUID IS NULL OR d.id>$3)
		ORDER BY d.id LIMIT $4`, asOf, driverID, cursor, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "live_unavailable", "Live operations are unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffLiveDriver, 0, limit+1)
	for rows.Next() {
		var item staffLiveDriver
		var requested, socketPresent bool
		if err := rows.Scan(&item.DriverID, &item.MarketCode, &item.Status, &requested, &socketPresent, &item.Latitude, &item.Longitude, &item.RecordedAt, &item.TripID); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "live_unavailable", "Live operations are unavailable")
			return
		}
		item.Presence = livePresence(asOf, requested, socketPresent, item.RecordedAt)
		// A stale coordinate remains useful for context, but its timestamp and
		// presence state must accompany it so clients cannot present it as live.
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "live_unavailable", "Live operations are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		id := items[len(items)-1].DriverID
		next = &id
	}
	ctx.Header("Cache-Control", "no-store")
	ctx.JSON(http.StatusOK, gin.H{"data": items, "meta": gin.H{"nextCursor": next, "asOf": asOf, "refreshAfterSeconds": livePollSeconds}})
}

func (a *adminAPI) overview(ctx *gin.Context) {
	asOf := time.Now().UTC()
	var waiting, active, openCases, reviewDrivers, currentSupply, staleSupply, disconnectedSupply, offlineSupply int64
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT
        (SELECT COUNT(*) FROM trips WHERE status='pending'),
        (SELECT COUNT(*) FROM trips WHERE status IN ('assigned','accepted','arrived','started')),
        (SELECT COUNT(*) FROM support_cases WHERE status NOT IN ('resolved','closed')),
        (SELECT COUNT(*) FROM driver_profiles WHERE admin_status='pending'),
        (SELECT COUNT(*) FROM drivers d JOIN driver_live_locations l ON l.driver_id=d.id WHERE d.online_requested AND l.recorded_at>$1::TIMESTAMPTZ-INTERVAL '30 seconds' AND EXISTS(SELECT 1 FROM driver_socket_sessions s WHERE s.driver_id=d.id AND s.expires_at>$1::TIMESTAMPTZ)),
        (SELECT COUNT(*) FROM drivers d JOIN driver_live_locations l ON l.driver_id=d.id WHERE d.online_requested AND l.recorded_at<=$1::TIMESTAMPTZ-INTERVAL '30 seconds' AND l.recorded_at>$1::TIMESTAMPTZ-INTERVAL '60 seconds' AND EXISTS(SELECT 1 FROM driver_socket_sessions s WHERE s.driver_id=d.id AND s.expires_at>$1::TIMESTAMPTZ)),
        (SELECT COUNT(*) FROM drivers d LEFT JOIN driver_live_locations l ON l.driver_id=d.id WHERE d.online_requested AND (l.recorded_at IS NULL OR l.recorded_at<=$1::TIMESTAMPTZ-INTERVAL '60 seconds' OR NOT EXISTS(SELECT 1 FROM driver_socket_sessions s WHERE s.driver_id=d.id AND s.expires_at>$1::TIMESTAMPTZ))),
        (SELECT COUNT(*) FROM drivers d WHERE NOT d.online_requested AND d.status IN ('offered','on_trip'))`, asOf).Scan(&waiting, &active, &openCases, &reviewDrivers, &currentSupply, &staleSupply, &disconnectedSupply, &offlineSupply)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "overview_unavailable", "Operations summary is unavailable")
		return
	}
	attention := make([]staffAttentionItem, 0)
	attentionCounts := gin.H{}
	attentionQueues := gin.H{}
	if adminHasPermission(ctx, permissionTripRead) {
		items, count, err := a.attentionQueue(ctx, "trip_request", "operations", "/admin/trips/", `FROM trips t WHERE t.status='pending'`, `t.id::TEXT`, `NULL::TEXT`, `t.created_at`, `'normal'`)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "overview_unavailable", "Attention items are unavailable")
			return
		}
		attention = append(attention, items...)
		attentionCounts["tripRequests"] = count
		attentionQueues["tripRequests"] = "/admin/trips?status=pending"
		waiting = count
	}
	if adminHasPermission(ctx, permissionSupportRead) {
		items, count, err := a.attentionQueue(ctx, "support_case", "support", "/admin/support/cases/", `FROM support_cases t WHERE t.status NOT IN ('resolved','closed')`, `t.id::TEXT`, `t.assigned_admin_id::TEXT`, `t.created_at`, `t.priority`)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "overview_unavailable", "Attention items are unavailable")
			return
		}
		attention = append(attention, items...)
		attentionCounts["supportCases"] = count
		attentionQueues["supportCases"] = "/admin/support/cases?status=attention"
		openCases = count
	}
	if adminHasPermission(ctx, permissionSafetyRead) {
		items, count, err := a.attentionQueue(ctx, "safety_incident", "safety", "/admin/safety/incidents/", `FROM safety_incidents t WHERE t.status<>'resolved'`, `t.id::TEXT`, `t.assigned_admin_id::TEXT`, `t.created_at`, `CASE WHEN t.severity IN ('high','critical') THEN 'high' ELSE 'normal' END`)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "overview_unavailable", "Attention items are unavailable")
			return
		}
		attention = append(attention, items...)
		attentionCounts["safetyIncidents"] = count
		attentionQueues["safetyIncidents"] = "/admin/safety/incidents?status=attention"
	}
	if adminHasPermission(ctx, permissionDriverRead) {
		items, count, err := a.attentionQueue(ctx, "driver_review", "compliance", "/admin/drivers/", `FROM driver_profiles t WHERE t.admin_status='pending'`, `t.driver_id::TEXT`, `NULL::TEXT`, `t.created_at`, `'normal'`)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "overview_unavailable", "Attention items are unavailable")
			return
		}
		attention = append(attention, items...)
		attentionCounts["driverReviews"] = count
		attentionQueues["driverReviews"] = "/admin/drivers?status=pending"
		reviewDrivers = count
	}
	ctx.Header("Cache-Control", "no-store")
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"asOf": asOf, "refreshAfterSeconds": livePollSeconds, "waitingRequests": waiting, "activeTrips": active, "openSupportCases": openCases, "pendingDriverReviews": reviewDrivers, "attentionItems": attention, "attentionCounts": attentionCounts, "attentionQueues": attentionQueues, "driverPresence": gin.H{"current": currentSupply, "stale": staleSupply, "disconnected": disconnectedSupply, "offline": offlineSupply}}})
}

func (a *adminAPI) attentionQueue(ctx *gin.Context, kind, owner, base, from, idExpr, assigneeExpr, timeExpr, priorityExpr string) ([]staffAttentionItem, int64, error) {
	var count int64
	var raw []byte
	query := `WITH filtered AS (SELECT ` + idExpr + ` AS entity_id,` + assigneeExpr + ` AS assigned_admin_id,` + timeExpr + ` AS created_at,` + priorityExpr + ` AS priority ` + from + `),
	page AS (SELECT * FROM filtered ORDER BY created_at DESC,entity_id DESC LIMIT 20)
	SELECT (SELECT COUNT(*) FROM filtered),COALESCE((SELECT jsonb_agg(jsonb_build_object('type',$1::TEXT,'entityId',entity_id,'priority',priority,'owner',$2::TEXT,'assignedAdminId',assigned_admin_id,'destination',$3::TEXT||entity_id,'createdAt',created_at) ORDER BY created_at DESC,entity_id DESC) FROM page),'[]'::jsonb)`
	if err := a.pool.QueryRow(ctx.Request.Context(), query, kind, owner, base).Scan(&count, &raw); err != nil {
		return nil, 0, err
	}
	items := make([]staffAttentionItem, 0)
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, 0, err
	}
	return items, count, nil
}

func (a *adminAPI) liveMarkers(ctx *gin.Context) {
	asOf := time.Now().UTC()
	requests := make([]gin.H, 0)
	if adminHasPermission(ctx, permissionGeofenceRead) {
		rows, err := a.pool.Query(ctx.Request.Context(), `SELECT t.id::TEXT,t.market_code,ST_Y(f.pickup::geometry),ST_X(f.pickup::geometry),f.created_at FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.status='pending' AND f.pickup IS NOT NULL ORDER BY t.created_at DESC,t.id DESC LIMIT 100`)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "live_unavailable", "Live markers are unavailable")
			return
		}
		for rows.Next() {
			var id string
			var market *string
			var latitude, longitude float64
			var recorded time.Time
			if err := rows.Scan(&id, &market, &latitude, &longitude, &recorded); err != nil {
				rows.Close()
				driverError(ctx, http.StatusServiceUnavailable, "live_unavailable", "Live markers are unavailable")
				return
			}
			requests = append(requests, gin.H{"type": "trip_request", "entityId": id, "marketCode": market, "latitude": latitude, "longitude": longitude, "recordedAt": recorded, "locationSource": "fare_pickup_snapshot", "destination": "/admin/trips/" + id})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			driverError(ctx, http.StatusServiceUnavailable, "live_unavailable", "Live markers are unavailable")
			return
		}
		rows.Close()
	}
	incidents := make([]gin.H, 0)
	if adminHasPermission(ctx, permissionSafetyRead) {
		rows, err := a.pool.Query(ctx.Request.Context(), `SELECT i.id::TEXT,c.trip_id::TEXT,ST_Y(l.location::geometry),ST_X(l.location::geometry),l.recorded_at,i.severity FROM safety_incidents i JOIN support_cases c ON c.id=i.case_id JOIN trips t ON t.id=c.trip_id JOIN driver_live_locations l ON l.driver_id=t.assigned_driver_id WHERE i.status<>'resolved' AND t.status IN ('assigned','accepted','arrived','started') AND l.recorded_at>$1::TIMESTAMPTZ-INTERVAL '30 seconds' AND EXISTS(SELECT 1 FROM driver_socket_sessions s WHERE s.driver_id=t.assigned_driver_id AND s.expires_at>$1) ORDER BY i.created_at DESC,i.id DESC LIMIT 100`, asOf)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "live_unavailable", "Live markers are unavailable")
			return
		}
		for rows.Next() {
			var id, tripID, severity string
			var latitude, longitude float64
			var recorded time.Time
			if err := rows.Scan(&id, &tripID, &latitude, &longitude, &recorded, &severity); err != nil {
				rows.Close()
				driverError(ctx, http.StatusServiceUnavailable, "live_unavailable", "Live markers are unavailable")
				return
			}
			incidents = append(incidents, gin.H{"type": "safety_incident", "entityId": id, "tripId": tripID, "severity": severity, "latitude": latitude, "longitude": longitude, "recordedAt": recorded, "locationSource": "assigned_driver_latest", "destination": "/admin/safety/incidents/" + id})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			driverError(ctx, http.StatusServiceUnavailable, "live_unavailable", "Live markers are unavailable")
			return
		}
		rows.Close()
	}
	ctx.Header("Cache-Control", "no-store")
	ctx.JSON(http.StatusOK, gin.H{"data": gin.H{"requests": requests, "incidents": incidents}, "meta": gin.H{"asOf": asOf, "refreshAfterSeconds": livePollSeconds, "requestLimit": 100, "incidentLimit": 100}})
}
