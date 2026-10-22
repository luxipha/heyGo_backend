package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

// "Missing" is a distinct band: an absent account is not a zero balance,
// and a missing/unapproved required document is not a valid expiry.
func partnerBand(raw string, allowed ...string) bool {
	if raw == "" || raw == "all" {
		return true
	}
	for _, value := range allowed {
		if raw == value {
			return true
		}
	}
	return false
}

// partnerSummary deliberately reports only aggregate operational counts. It
// does not expose individual locations, contacts, or financial balances.
func (a *adminAPI) partnerSummary(ctx *gin.Context) {
	asOf := time.Now().UTC()
	var total, active, online, pending, restricted, lowBalance, documentsExpiring int
	err := a.pool.QueryRow(ctx.Request.Context(), `WITH state AS (
		SELECT p.admin_status,a.balance_kobo,
			COALESCE(d.online_requested,FALSE)
			AND EXISTS(SELECT 1 FROM driver_socket_sessions s WHERE s.driver_id=p.driver_id AND s.expires_at>$1)
			AND COALESCE(l.recorded_at>$1::TIMESTAMPTZ-INTERVAL '30 seconds',FALSE) AS online_current,
			CASE WHEN docs.approved_count<5 THEN 'missing' WHEN docs.next_expiry<=$1 THEN 'expired'
				WHEN docs.next_expiry<=$1::TIMESTAMPTZ+INTERVAL '30 days' THEN 'within_30_days' ELSE 'valid' END AS expiry_band
		FROM driver_profiles p
		LEFT JOIN drivers d ON d.id=p.driver_id
		LEFT JOIN driver_live_locations l ON l.driver_id=p.driver_id
		LEFT JOIN driver_operating_accounts a ON a.driver_id=p.driver_id
		CROSS JOIN LATERAL (SELECT COUNT(*) FILTER(WHERE status='approved') AS approved_count,
			MIN(expires_at) FILTER(WHERE status='approved') AS next_expiry FROM (
			SELECT DISTINCT ON(type) type,status,expires_at FROM driver_documents
			WHERE driver_id=p.driver_id AND type IN ('driver_license','vehicle_license','roadworthiness','auto_insurance','hackney_permit')
			ORDER BY type,submitted_at DESC,id DESC
		) latest) docs
	)
	SELECT COUNT(*),
		COUNT(*) FILTER(WHERE admin_status='approved'),
		COUNT(*) FILTER(WHERE online_current),
		COUNT(*) FILTER(WHERE admin_status='pending'),
		COUNT(*) FILTER(WHERE admin_status='rejected'),
		COUNT(*) FILTER(WHERE balance_kobo<0),
		COUNT(*) FILTER(WHERE expiry_band='within_30_days')
	FROM state`, asOf).Scan(&total, &active, &online, &pending, &restricted, &lowBalance, &documentsExpiring)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	ctx.Header("X-As-Of", asOf.Format(time.RFC3339Nano))
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{
		"total": total, "active": active, "online": online,
		"pending": pending, "restricted": restricted, "lowBalance": lowBalance,
		"documentsExpiring": documentsExpiring,
	}})
}

func (a *adminAPI) listPartners(ctx *gin.Context) {
	status := ctx.DefaultQuery("status", "pending")
	if !partnerBand(status, "pending", "approved", "rejected") {
		driverError(ctx, http.StatusBadRequest, "invalid_status", "Status is invalid")
		return
	}
	online := ctx.DefaultQuery("online", "all")
	if !partnerBand(online, "current", "not_current") {
		driverError(ctx, http.StatusBadRequest, "invalid_online", "Online filter is invalid")
		return
	}
	balance := ctx.DefaultQuery("balance", "all")
	if !partnerBand(balance, "negative", "zero", "positive", "missing") {
		driverError(ctx, http.StatusBadRequest, "invalid_balance", "Balance band is invalid")
		return
	}
	expiry := ctx.DefaultQuery("expiry", "all")
	if !partnerBand(expiry, "expired", "within_30_days", "valid", "missing") {
		driverError(ctx, http.StatusBadRequest, "invalid_expiry", "Expiry band is invalid")
		return
	}
	market := strings.TrimSpace(ctx.Query("zone"))
	if len(market) > 64 {
		driverError(ctx, http.StatusBadRequest, "invalid_zone", "Operating market is invalid")
		return
	}
	vehicle := strings.TrimSpace(ctx.Query("vehicle"))
	if len(vehicle) > 100 {
		driverError(ctx, http.StatusBadRequest, "invalid_vehicle", "Vehicle filter is invalid")
		return
	}
	search := strings.TrimSpace(ctx.Query("q"))
	if len(search) > 100 {
		driverError(ctx, http.StatusBadRequest, "invalid_search", "Partner search is too long")
		return
	}
	var searchID any
	searchName := search
	if parsed, err := uuid.Parse(search); err == nil {
		searchID = parsed
		searchName = ""
	}
	limit, err := parseDriverTripHistoryLimit(ctx.DefaultQuery("limit", "100"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	var cursorTime any
	var cursorID any
	if raw := ctx.Query("cursor"); raw != "" {
		cursor, err := decodeDriverTripHistoryCursor(raw)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Partner cursor is invalid")
			return
		}
		cursorTime, cursorID = cursor.OccurredAt, cursor.ID
	}
	asOf := time.Now().UTC()
	rows, err := a.pool.Query(ctx.Request.Context(), `WITH partner_state AS (
		SELECT p.driver_id,COALESCE(NULLIF((SELECT BTRIM(CONCAT_WS(' ',ip.first_name,ip.last_name)) FROM driver_identity_profiles ip WHERE ip.driver_id=p.driver_id),''),NULLIF(BTRIM(p.display_name),''),'') AS display_name,p.application_number,u.phone_e164,u.email,p.admin_status,p.manual_verification_status,p.updated_at,
			d.online_market_code,v.make,v.model,v.plate,
			COALESCE(d.online_requested,FALSE) AND EXISTS(SELECT 1 FROM driver_socket_sessions s WHERE s.driver_id=p.driver_id AND s.expires_at>$1)
			AND COALESCE(l.recorded_at>$1::TIMESTAMPTZ-INTERVAL '30 seconds',FALSE) AS online_current,
			a.balance_kobo,docs.next_expiry,
			CASE WHEN a.driver_id IS NULL THEN 'missing' WHEN a.balance_kobo<0 THEN 'negative' WHEN a.balance_kobo=0 THEN 'zero' ELSE 'positive' END AS balance_band,
			CASE WHEN docs.approved_count<5 THEN 'missing' WHEN docs.next_expiry<=$1 THEN 'expired'
				WHEN docs.next_expiry<=$1::TIMESTAMPTZ+INTERVAL '30 days' THEN 'within_30_days' ELSE 'valid' END AS expiry_band
		FROM driver_profiles p JOIN users u ON u.id=p.driver_id
			LEFT JOIN drivers d ON d.id=p.driver_id
			LEFT JOIN driver_vehicles v ON v.driver_id=p.driver_id
		LEFT JOIN driver_live_locations l ON l.driver_id=p.driver_id
		LEFT JOIN driver_operating_accounts a ON a.driver_id=p.driver_id
		CROSS JOIN LATERAL (SELECT COUNT(*) FILTER(WHERE status='approved') AS approved_count,
			MIN(expires_at) FILTER(WHERE status='approved') AS next_expiry FROM (
			SELECT DISTINCT ON(type) type,status,expires_at FROM driver_documents
			WHERE driver_id=p.driver_id AND type IN ('driver_license','vehicle_license','roadworthiness','auto_insurance','hackney_permit')
			ORDER BY type,submitted_at DESC,id DESC
		) latest) docs
	)
	SELECT driver_id::TEXT,display_name,COALESCE(phone_e164,''),COALESCE(email,''),admin_status,manual_verification_status,
		updated_at,online_market_code,online_current,balance_kobo,balance_band,next_expiry,expiry_band,
		COALESCE(make,''),COALESCE(model,''),COALESCE(plate,''),application_number
	FROM partner_state
	WHERE ($2='all' OR admin_status=$2)
	AND ($3::UUID IS NULL OR driver_id=$3)
	AND ($4='' OR left(lower(display_name),char_length($4))=lower($4))
	AND ($5='all' OR ($5='current' AND online_current) OR ($5='not_current' AND NOT online_current))
	AND ($6='' OR online_market_code=$6)
	AND ($7='all' OR balance_band=$7)
	AND ($8='all' OR expiry_band=$8)
	AND ($9='' OR lower(make)=lower($9))
	AND ($10::TIMESTAMPTZ IS NULL OR (updated_at,driver_id)<($10,$11::UUID))
	ORDER BY updated_at DESC,driver_id DESC LIMIT $12`, asOf, status, searchID, searchName, online, market, balance, expiry, vehicle, cursorTime, cursorID, limit+1)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit+1)
	for rows.Next() {
		var id, name, phone, email, state, manual, balanceBand, expiryBand, make, model, plate, applicationNumber string
		var updated time.Time
		var marketCode *string
		var current bool
		var balanceKobo *int64
		var nextExpiry *time.Time
		if err := rows.Scan(&id, &name, &phone, &email, &state, &manual, &updated, &marketCode, &current, &balanceKobo, &balanceBand, &nextExpiry, &expiryBand, &make, &model, &plate, &applicationNumber); err != nil {
			reviewFailure(ctx)
			return
		}
		item := gin.H{"driverId": id, "displayName": name, "applicationNumber": applicationNumber, "adminStatus": state,
			"manualVerificationStatus": manual, "updatedAt": updated, "onlineCurrent": current,
			"zone": marketCode, "balanceBand": balanceBand, "expiryBand": expiryBand, "nextDocumentExpiryAt": nextExpiry,
			"vehicle": gin.H{"make": make, "model": model, "plate": plate}}
		if adminHasPermission(ctx, permissionDriverContact) {
			item["phone"], item["email"] = phone, email
		}
		if adminHasPermission(ctx, permissionFinanceRead) {
			item["balanceKobo"] = balanceKobo
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		reviewFailure(ctx)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		value := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: last["updatedAt"].(time.Time), ID: last["driverId"].(string)})
		next = &value
	}
	ctx.Header("Cache-Control", "no-store")
	ctx.Header("X-As-Of", asOf.Format(time.RFC3339Nano))
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}
