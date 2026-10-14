package handler

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (a *adminAPI) partnerContext(ctx *gin.Context, rawID string) (gin.H, error) {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return nil, err
	}
	links := gin.H{}
	links["supportDetailPermitted"] = adminHasPermission(ctx, permissionSupportRead)
	links["safetyDetailPermitted"] = adminHasPermission(ctx, permissionSafetyRead)
	performance, err := readDriverPerformance(ctx.Request.Context(), a.pool, rawID)
	if err != nil {
		return nil, err
	}
	links["performance"] = performance
	queries := map[string]string{
		"trips": `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',x.id,'status',x.status,'createdAt',x.created_at,
			'completedAt',x.completed_at,'destination','/admin/trips/'||x.id::TEXT) ORDER BY x.created_at DESC,x.id DESC),'[]'::JSONB)
			FROM (SELECT id,status,created_at,completed_at FROM trips WHERE assigned_driver_id=$1 ORDER BY created_at DESC,id DESC LIMIT 20) x`,
		"supportCases": `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',x.id,'tripId',x.trip_id,'status',x.status,
			'priority',x.priority,'topicCode',x.topic_code,'destination','/admin/support/cases/'||x.id::TEXT)
			ORDER BY x.created_at DESC,x.id DESC),'[]'::JSONB) FROM (
			SELECT c.id,c.trip_id,c.status,c.priority,c.topic_code,c.created_at FROM support_cases c
			WHERE c.opened_by=$1 OR EXISTS(SELECT 1 FROM trips t WHERE t.id=c.trip_id AND t.assigned_driver_id=$1)
			ORDER BY c.created_at DESC,c.id DESC LIMIT 20) x`,
		"safetyIncidents": `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',x.id,'caseId',x.case_id,'tripId',x.trip_id,
			'status',x.status,'severity',x.severity,'destination','/admin/safety/incidents/'||x.id::TEXT)
			ORDER BY x.created_at DESC,x.id DESC),'[]'::JSONB) FROM (
			SELECT i.id,i.case_id,c.trip_id,i.status,i.severity,i.created_at FROM safety_incidents i
			JOIN support_cases c ON c.id=i.case_id
			WHERE c.opened_by=$1 OR EXISTS(SELECT 1 FROM trips t WHERE t.id=c.trip_id AND t.assigned_driver_id=$1)
			ORDER BY i.created_at DESC,i.id DESC LIMIT 20) x`,
	}
	for key, query := range queries {
		value, err := a.staffJSON(ctx, query, id)
		if err != nil {
			return nil, err
		}
		links[key] = value
	}
	// A Compliance reviewer can identify a balance band without reading the
	// exact financial amount or ledger. Finance and Super Admin receive kobo.
	var balanceKobo *int64
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1`, id).Scan(&balanceKobo)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	band := "missing"
	if balanceKobo != nil {
		switch {
		case *balanceKobo < 0:
			band = "negative"
		case *balanceKobo == 0:
			band = "zero"
		default:
			band = "positive"
		}
	}
	links["balanceBand"] = band
	if adminHasPermission(ctx, permissionFinanceRead) {
		links["balanceKobo"] = balanceKobo
	}
	var marketCode *string
	var onlineRequested, socketPresent bool
	var recordedAt *time.Time
	asOf := time.Now().UTC()
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT d.online_market_code,d.online_requested,
		EXISTS(SELECT 1 FROM driver_socket_sessions s WHERE s.driver_id=d.id AND s.expires_at>$2),
		l.recorded_at FROM drivers d LEFT JOIN driver_live_locations l ON l.driver_id=d.id WHERE d.id=$1`, id, asOf).Scan(&marketCode, &onlineRequested, &socketPresent, &recordedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	links["operatingMarketCode"] = marketCode
	links["presence"] = livePresence(asOf, onlineRequested, socketPresent, recordedAt)
	links["presenceAsOf"] = asOf
	return links, nil
}
