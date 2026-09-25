package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func registerAdminOperatingBalanceRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/operating-balance/diagnostics", a.operatingBalanceDiagnostics)
	group.GET("/operating-balance/policies", a.listOperatingBalancePolicies)
	group.GET("/operating-balance/policies/:id", a.operatingBalancePolicyDetail)
	group.POST("/operating-balance/policies", a.requireCSRF, a.createOperatingBalancePolicy)
	group.POST("/operating-balance/policies/:id/approve", a.requireCSRF, a.approveOperatingBalancePolicy)
	group.POST("/operating-balance/policies/:id/retirement-requests", a.requireCSRF, a.requestOperatingBalancePolicyRetirement)
	group.POST("/operating-balance/policies/:id/retirement-requests/:requestID/approve", a.requireCSRF, a.approveOperatingBalancePolicyRetirement)
}

func (a *adminAPI) operatingBalanceDiagnostics(ctx *gin.Context) {
	market := strings.ToUpper(strings.TrimSpace(ctx.Query("marketCode")))
	if !geofenceCodePattern.MatchString(market) {
		driverError(ctx, http.StatusBadRequest, "invalid_market", "Valid market code is required")
		return
	}
	var policies int
	var minimum int64
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT COUNT(*),COALESCE(MIN(minimum_kobo),0)
		FROM operating_balance_policies WHERE market_code=$1 AND status='approved'
		AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())`, market).Scan(&policies, &minimum); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "balance_policies_unavailable", "Operating Balance policies are unavailable")
		return
	}
	if policies != 1 {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"marketCode": market, "ready": false, "code": "MINIMUM_BALANCE_POLICY_MISSING"}})
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"marketCode": market, "ready": true, "minimumKobo": minimum}})
}

func (a *adminAPI) listOperatingBalancePolicies(ctx *gin.Context) {
	market := strings.ToUpper(strings.TrimSpace(ctx.Query("marketCode")))
	if market != "" && !geofenceCodePattern.MatchString(market) {
		driverError(ctx, http.StatusBadRequest, "invalid_market", "Valid market code is required")
		return
	}
	cursor := ctx.Query("cursor")
	if cursor != "" {
		if _, err := uuid.Parse(cursor); err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Valid cursor is required")
			return
		}
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT p.id::TEXT,p.market_code,p.version,p.minimum_kobo,p.warning_kobo,
		p.allow_negative,p.maximum_negative_kobo,p.topup_minimum_kobo,p.topup_maximum_kobo,p.status,p.effective_from,p.effective_until,p.created_at
		FROM operating_balance_policies p WHERE ($1='' OR p.market_code=$1)
		AND ($2='' OR (p.created_at,p.id)<(SELECT c.created_at,c.id FROM operating_balance_policies c WHERE c.id=NULLIF($2,'')::UUID))
		ORDER BY p.created_at DESC,p.id DESC LIMIT 51`, market, cursor)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "balance_policies_unavailable", "Operating Balance policies are unavailable")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, 50)
	for rows.Next() {
		var id, code, state string
		var version int
		var minimum, warning, maximum int64
		var topupMinimum, topupMaximum *int64
		var allowNegative bool
		var from, created time.Time
		var until *time.Time
		if err := rows.Scan(&id, &code, &version, &minimum, &warning, &allowNegative, &maximum, &topupMinimum, &topupMaximum, &state, &from, &until, &created); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "balance_policies_unavailable", "Operating Balance policies are unavailable")
			return
		}
		items = append(items, gin.H{"id": id, "marketCode": code, "version": version, "minimumKobo": minimum,
			"warningKobo": warning, "allowNegative": allowNegative, "maximumNegativeKobo": maximum,
			"topupMinimumKobo": topupMinimum, "topupMaximumKobo": topupMaximum,
			"status": state, "effectiveFrom": from, "effectiveUntil": until, "createdAt": created})
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "balance_policies_unavailable", "Operating Balance policies are unavailable")
		return
	}
	var next *string
	if len(items) > 50 {
		value := items[49]["id"].(string)
		next = &value
		items = items[:50]
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) operatingBalancePolicyDetail(ctx *gin.Context) {
	id, ok := geofenceID(ctx, "id")
	if !ok {
		return
	}
	var code, state, creator string
	var approver *string
	var version int
	var minimum, warning, maximum int64
	var topupMinimum, topupMaximum *int64
	var allowNegative bool
	var from, created time.Time
	var until, approvedAt *time.Time
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT market_code,version,minimum_kobo,warning_kobo,allow_negative,
		maximum_negative_kobo,topup_minimum_kobo,topup_maximum_kobo,status,effective_from,effective_until,created_by::TEXT,approved_by::TEXT,approved_at,created_at
		FROM operating_balance_policies WHERE id=$1::UUID`, id).Scan(&code, &version, &minimum, &warning, &allowNegative,
		&maximum, &topupMinimum, &topupMaximum, &state, &from, &until, &creator, &approver, &approvedAt, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "balance_policy_not_found", "Operating Balance policy was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "balance_policies_unavailable", "Operating Balance policy is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "marketCode": code, "version": version,
		"minimumKobo": minimum, "warningKobo": warning, "allowNegative": allowNegative,
		"topupMinimumKobo": topupMinimum, "topupMaximumKobo": topupMaximum,
		"maximumNegativeKobo": maximum, "status": state, "effectiveFrom": from, "effectiveUntil": until,
		"createdBy": creator, "approvedBy": approver, "approvedAt": approvedAt, "createdAt": created}})
}

func (a *adminAPI) createOperatingBalancePolicy(ctx *gin.Context) {
	var body struct {
		MarketCode          string     `json:"marketCode"`
		MinimumKobo         *int64     `json:"minimumKobo"`
		WarningKobo         *int64     `json:"warningKobo"`
		AllowNegative       *bool      `json:"allowNegative"`
		MaximumNegativeKobo *int64     `json:"maximumNegativeKobo"`
		TopupMinimumKobo    *int64     `json:"topupMinimumKobo"`
		TopupMaximumKobo    *int64     `json:"topupMaximumKobo"`
		EffectiveFrom       time.Time  `json:"effectiveFrom"`
		EffectiveUntil      *time.Time `json:"effectiveUntil"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_balance_policy", "Valid Operating Balance policy is required")
		return
	}
	body.MarketCode = strings.ToUpper(strings.TrimSpace(body.MarketCode))
	if !geofenceCodePattern.MatchString(body.MarketCode) || body.MinimumKobo == nil || body.WarningKobo == nil ||
		body.AllowNegative == nil || body.MaximumNegativeKobo == nil || body.TopupMinimumKobo == nil || body.TopupMaximumKobo == nil || *body.MinimumKobo < 0 ||
		*body.WarningKobo < *body.MinimumKobo || *body.MaximumNegativeKobo < 0 ||
		*body.TopupMinimumKobo <= 0 || *body.TopupMaximumKobo < *body.TopupMinimumKobo ||
		(!*body.AllowNegative && *body.MaximumNegativeKobo != 0) || body.EffectiveFrom.IsZero() ||
		(body.EffectiveUntil != nil && !body.EffectiveUntil.After(body.EffectiveFrom)) {
		driverError(ctx, http.StatusBadRequest, "invalid_balance_policy", "Market, amounts, negative-balance rule, and effective dates are required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	if _, err := tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, "balance-policy:"+body.MarketCode); err != nil {
		reviewFailure(ctx)
		return
	}
	var version int
	if err := tx.QueryRow(ctx.Request.Context(), `SELECT COALESCE(MAX(version),0)+1 FROM operating_balance_policies WHERE market_code=$1`, body.MarketCode).Scan(&version); err != nil {
		reviewFailure(ctx)
		return
	}
	id := uuid.NewString()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO operating_balance_policies(id,market_code,version,minimum_kobo,warning_kobo,
		allow_negative,maximum_negative_kobo,topup_minimum_kobo,topup_maximum_kobo,status,effective_from,effective_until,created_by)
		VALUES($1::UUID,$2,$3,$4,$5,$6,$7,$8,$9,'draft',$10,$11,$12::UUID)`, id, body.MarketCode, version,
		*body.MinimumKobo, *body.WarningKobo, *body.AllowNegative, *body.MaximumNegativeKobo,
		*body.TopupMinimumKobo, *body.TopupMaximumKobo, body.EffectiveFrom.UTC(), body.EffectiveUntil, currentAdmin(ctx).ID)
	if err != nil || adminAudit(ctx, tx, "balance_policy_draft_created", "", gin.H{"policyId": id, "marketCode": body.MarketCode, "version": version}) != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id, "status": "draft", "version": version}})
}

func (a *adminAPI) approveOperatingBalancePolicy(ctx *gin.Context) {
	id, ok := geofenceID(ctx, "id")
	if !ok {
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var market, state, creator string
	var from time.Time
	var until *time.Time
	var topupMinimum, topupMaximum *int64
	err = tx.QueryRow(ctx.Request.Context(), `SELECT market_code,status,created_by::TEXT,effective_from,effective_until,topup_minimum_kobo,topup_maximum_kobo
		FROM operating_balance_policies WHERE id=$1::UUID FOR UPDATE`, id).Scan(&market, &state, &creator, &from, &until, &topupMinimum, &topupMaximum)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "balance_policy_not_found", "Operating Balance policy was not found")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if state != "draft" || creator == currentAdmin(ctx).ID || (until != nil && !until.After(time.Now())) || topupMinimum == nil || topupMaximum == nil {
		driverError(ctx, http.StatusConflict, "balance_policy_approval_unavailable", "A different staff member must approve a current draft")
		return
	}
	if _, err := tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, "approve-balance-policy:"+market); err != nil {
		reviewFailure(ctx)
		return
	}
	var approvedMarket, overlap bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM trip_geofences g
		JOIN operating_balance_policies d ON d.id=$1::UUID
		WHERE g.kind='market' AND g.code=d.market_code AND g.status='approved'
		AND g.effective_from<=d.effective_from
		AND (g.effective_until IS NULL OR g.effective_until>d.effective_from))`, id).Scan(&approvedMarket)
	if err == nil {
		err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM operating_balance_policies p
			JOIN operating_balance_policies d ON d.id=$1::UUID WHERE p.id<>d.id AND p.market_code=d.market_code
			AND p.status='approved' AND (p.effective_until IS NULL OR p.effective_until>d.effective_from)
			AND (d.effective_until IS NULL OR d.effective_until>p.effective_from))`, id).Scan(&overlap)
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if !approvedMarket || overlap {
		driverError(ctx, http.StatusConflict, "balance_policy_unpublishable", "An approved market is required and policy periods must not overlap")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE operating_balance_policies SET status='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1::UUID AND status='draft'`, id, currentAdmin(ctx).ID)
	if err != nil || adminAudit(ctx, tx, "balance_policy_approved", "", gin.H{"policyId": id, "marketCode": market}) != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "status": "approved"}})
}

func (a *adminAPI) requestOperatingBalancePolicyRetirement(ctx *gin.Context) {
	id, ok := geofenceID(ctx, "id")
	if !ok {
		return
	}
	var body struct {
		EffectiveUntil time.Time `json:"effectiveUntil"`
	}
	if ctx.ShouldBindJSON(&body) != nil || body.EffectiveUntil.Before(time.Now()) {
		driverError(ctx, http.StatusBadRequest, "invalid_retirement", "A future effectiveUntil is required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	requestID := uuid.NewString()
	tag, err := tx.Exec(ctx.Request.Context(), `INSERT INTO operating_balance_policy_retirements(id,policy_id,effective_until,requested_by,status)
		SELECT $1::UUID,id,$3,$4::UUID,'pending' FROM operating_balance_policies
		WHERE id=$2::UUID AND status='approved' AND effective_until IS NULL AND effective_from<$3`, requestID, id, body.EffectiveUntil.UTC(), currentAdmin(ctx).ID)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(ctx, http.StatusConflict, "retirement_unavailable", "Policy cannot be retired")
		return
	}
	if adminAudit(ctx, tx, "balance_policy_retirement_requested", "", gin.H{"policyId": id, "requestId": requestID, "effectiveUntil": body.EffectiveUntil.UTC()}) != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": requestID, "status": "pending"}})
}

func (a *adminAPI) approveOperatingBalancePolicyRetirement(ctx *gin.Context) {
	id, ok := geofenceID(ctx, "id")
	if !ok {
		return
	}
	requestID, ok := geofenceID(ctx, "requestID")
	if !ok {
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var requester string
	var until time.Time
	err = tx.QueryRow(ctx.Request.Context(), `SELECT requested_by::TEXT,effective_until FROM operating_balance_policy_retirements
		WHERE id=$1::UUID AND policy_id=$2::UUID AND status='pending' FOR UPDATE`, requestID, id).Scan(&requester, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "retirement_not_found", "Pending retirement was not found")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if requester == currentAdmin(ctx).ID || until.Before(time.Now()) {
		driverError(ctx, http.StatusConflict, "retirement_approval_unavailable", "A different staff member must approve before the retirement time")
		return
	}
	tag, err := tx.Exec(ctx.Request.Context(), `UPDATE operating_balance_policies SET effective_until=$2 WHERE id=$1::UUID AND status='approved' AND effective_until IS NULL`, id, until)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(ctx, http.StatusConflict, "retirement_unavailable", "Policy cannot be retired")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE operating_balance_policy_retirements SET status='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1::UUID`, requestID, currentAdmin(ctx).ID)
	if err != nil || adminAudit(ctx, tx, "balance_policy_retirement_approved", "", gin.H{"policyId": id, "requestId": requestID, "effectiveUntil": until}) != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "effectiveUntil": until}})
}
