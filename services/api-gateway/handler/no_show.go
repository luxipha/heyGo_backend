package handler

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	gatewayauth "github.com/cprakhar/uber-clone/services/api-gateway/auth"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/observe/correlation"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func registerNoShowDriverRoutes(g *gin.RouterGroup, a *driverAPI) {
	g.GET("/trips/:tripID/no-show", a.noShowEligibility)
	g.POST("/trips/:tripID/no-show-claims", a.submitNoShowClaim)
	g.GET("/no-show-claims", a.driverNoShowClaims)
}

func registerNoShowRiderRoutes(authenticated *gin.RouterGroup, pool *pgxpool.Pool) {
	rider := authenticated.Group("/rider", gatewayauth.RequireRole("rider"))
	rider.GET("/no-show-debts", func(ctx *gin.Context) {
		user, _ := gatewayauth.CurrentUser(ctx)
		rows, err := pool.Query(ctx.Request.Context(), `SELECT d.id::TEXT,d.amount_kobo,d.created_at,c.market_code,c.trip_id::TEXT
			FROM rider_no_show_debts d JOIN driver_no_show_claims c ON c.id=d.claim_id
			WHERE d.rider_id=$1::UUID AND d.status='due' ORDER BY d.created_at,d.id`, user.ID)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "no_show_debts_unavailable", "No-show debts are unavailable")
			return
		}
		defer rows.Close()
		items := []gin.H{}
		var total int64
		for rows.Next() {
			var id, market, trip string
			var amount int64
			var created time.Time
			if rows.Scan(&id, &amount, &created, &market, &trip) != nil {
				driverError(ctx, http.StatusServiceUnavailable, "no_show_debts_unavailable", "No-show debts are unavailable")
				return
			}
			total += amount
			items = append(items, gin.H{"id": id, "tripId": trip, "marketCode": market, "amountKobo": amount, "currency": "NGN", "createdAt": created})
		}
		if rows.Err() != nil {
			driverError(ctx, http.StatusServiceUnavailable, "no_show_debts_unavailable", "No-show debts are unavailable")
			return
		}
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"debts": items, "totalDueKobo": total, "currency": "NGN"}})
	})
	registerRiderNoShowPaymentRoutes(rider, pool)
}

func (a *driverAPI) noShowEligibility(ctx *gin.Context) {
	tripID := ctx.Param("tripID")
	if _, err := uuid.Parse(tripID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	var riderID, status, market string
	var arrivedAt *time.Time
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT rider_id::TEXT,status,COALESCE(market_code,''),arrived_at
		FROM trips WHERE id=$1::UUID AND assigned_driver_id=$2::UUID`, tripID, driverID(ctx)).Scan(&riderID, &status, &market, &arrivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "trip_not_found", "Trip was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show eligibility is unavailable")
		return
	}
	var claimID, claimStatus string
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,status FROM driver_no_show_claims WHERE trip_id=$1::UUID`, tripID).Scan(&claimID, &claimStatus)
	if err == nil {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"tripId": tripID, "eligible": false, "status": claimStatus, "claimId": claimID}})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show eligibility is unavailable")
		return
	}
	if arrivedAt == nil || status != "arrived" {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"tripId": tripID, "eligible": false, "reason": "driver_not_arrived"}})
		return
	}
	var policyID string
	var waitMinutes int
	var fee int64
	var dbNow time.Time
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,wait_minutes,fee_kobo FROM no_show_policies
		WHERE market_code=$1 AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())
		ORDER BY version DESC LIMIT 1`, market).Scan(&policyID, &waitMinutes, &fee)
	if errors.Is(err, pgx.ErrNoRows) {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"tripId": tripID, "eligible": false, "reason": "market_policy_not_configured"}})
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show eligibility is unavailable")
		return
	}
	if err = a.pool.QueryRow(ctx.Request.Context(), `SELECT NOW()`).Scan(&dbNow); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show eligibility is unavailable")
		return
	}
	eligibleAt := arrivedAt.Add(time.Duration(waitMinutes) * time.Minute)
	if dbNow.Before(eligibleAt) {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"tripId": tripID, "eligible": false, "reason": "wait_period_not_elapsed", "arrivedAt": arrivedAt, "eligibleAt": eligibleAt, "waitMinutes": waitMinutes, "feeKobo": fee, "currency": "NGN"}})
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"tripId": tripID, "eligible": true, "arrivedAt": arrivedAt, "eligibleAt": eligibleAt, "waitMinutes": waitMinutes, "feeKobo": fee, "currency": "NGN"}})
}

func (a *driverAPI) submitNoShowClaim(ctx *gin.Context) {
	tripID := ctx.Param("tripID")
	if _, err := uuid.Parse(tripID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	driver := driverID(ctx)
	dbctx := ctx.Request.Context()
	tx, err := a.pool.Begin(dbctx)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	defer tx.Rollback(dbctx)
	var rider, status, market string
	var arrivedAt *time.Time
	err = tx.QueryRow(dbctx, `SELECT rider_id::TEXT,status,COALESCE(market_code,''),arrived_at FROM trips
		WHERE id=$1::UUID AND assigned_driver_id=$2::UUID FOR UPDATE`, tripID, driver).Scan(&rider, &status, &market, &arrivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "trip_not_found", "Trip was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	if status != "arrived" || arrivedAt == nil {
		driverError(ctx, http.StatusConflict, "no_show_not_eligible", "Only an arrived, active trip can have a no-show claim")
		return
	}
	var policyID string
	var waitMinutes int
	var fee int64
	var dbNow time.Time
	err = tx.QueryRow(dbctx, `SELECT id::TEXT,wait_minutes,fee_kobo FROM no_show_policies
		WHERE market_code=$1 AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())
		ORDER BY version DESC LIMIT 1 FOR SHARE`, market).Scan(&policyID, &waitMinutes, &fee)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusConflict, "no_show_policy_missing", "No-show claims are not enabled for this market")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	if err = tx.QueryRow(dbctx, `SELECT NOW()`).Scan(&dbNow); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	eligibleAt := arrivedAt.Add(time.Duration(waitMinutes) * time.Minute)
	if dbNow.Before(eligibleAt) {
		driverError(ctx, http.StatusConflict, "no_show_wait_not_elapsed", "The configured pickup wait period has not elapsed")
		return
	}
	claimID := uuid.NewString()
	_, err = tx.Exec(dbctx, `INSERT INTO driver_no_show_claims(id,trip_id,driver_id,rider_id,market_code,policy_id,fee_kobo,arrived_at,eligible_at,status)
		VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,$5,$6::UUID,$7,$8,$9,'pending')`, claimID, tripID, driver, rider, market, policyID, fee, arrivedAt, eligibleAt)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			driverError(ctx, http.StatusConflict, "no_show_claim_exists", "A no-show claim already exists for this trip")
		} else {
			driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		}
		return
	}
	// Submitting a claim ends this trip as a no-show candidate. Admin approval
	// creates a rider debt; it does not immediately move money.
	if _, err = tx.Exec(dbctx, `UPDATE trips SET status='cancelled',cancellation_reason='no_show_claim_submitted',cancellation_actor_id=$2::UUID,cancelled_at=NOW(),updated_at=NOW(),version=version+1
		WHERE id=$1::UUID AND status='arrived'`, tripID, driver); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	if _, err = tx.Exec(dbctx, `UPDATE trip_no_show_debt_settlements SET status='released' WHERE trip_id=$1::UUID AND status='pending'`, tripID); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	if _, err = tx.Exec(dbctx, `UPDATE driver_assignments SET status='cancelled',responded_at=NOW() WHERE trip_id=$1::UUID AND driver_id=$2::UUID AND status='accepted'`, tripID, driver); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	if _, err = tx.Exec(dbctx, `UPDATE drivers SET status=CASE WHEN online_requested AND driver_balance_eligible(id,online_market_code)
		AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE) THEN 'available' ELSE 'offline' END,
		available=online_requested AND driver_balance_eligible(id,online_market_code)
		AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE),updated_at=NOW() WHERE id=$1::UUID`, driver); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	payload := gin.H{"tripId": tripID, "claimId": claimID, "status": "pending", "feeKobo": fee, "currency": "NGN"}
	if _, err = tx.Exec(dbctx, `INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,correlation_id,attempt)
		VALUES($1::UUID,$2,$3::UUID,jsonb_build_object('trip',jsonb_build_object('id',$1::TEXT,'status','cancelled','reasonCode','no_show_claim_submitted')),$4,0),
		($1::UUID,$2,$5::UUID,jsonb_build_object('trip',jsonb_build_object('id',$1::TEXT,'status','cancelled','reasonCode','no_show_claim_submitted')),$4,0)
		ON CONFLICT DO NOTHING`, tripID, contracts.TripEventCancelled, rider, correlation.FromContext(dbctx), driver); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	if _, err = insertDriverEvent(ctx, tx, driver, contracts.TripEventNoShowClaimUpdated, payload); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	if _, err = insertDriverEvent(ctx, tx, rider, contracts.TripEventNoShowClaimUpdated, payload); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	if err = tx.Commit(dbctx); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claim could not be submitted")
		return
	}
	ctx.JSON(http.StatusAccepted, contracts.APIResponse{Data: payload})
}

func (a *driverAPI) driverNoShowClaims(ctx *gin.Context) {
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT c.id::TEXT,c.trip_id::TEXT,c.status,c.fee_kobo,c.market_code,c.submitted_at,c.reviewed_at,
		COALESCE(d.status,'') FROM driver_no_show_claims c LEFT JOIN rider_no_show_debts d ON d.claim_id=c.id
		WHERE c.driver_id=$1::UUID ORDER BY c.submitted_at DESC,c.id DESC LIMIT 100`, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claims are unavailable")
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var id, tripID, status, market, debtStatus string
		var fee int64
		var submitted time.Time
		var reviewed *time.Time
		if rows.Scan(&id, &tripID, &status, &fee, &market, &submitted, &reviewed, &debtStatus) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claims are unavailable")
			return
		}
		items = append(items, gin.H{"id": id, "tripId": tripID, "status": status, "feeKobo": fee, "currency": "NGN", "marketCode": market, "submittedAt": submitted, "reviewedAt": reviewed, "debtStatus": debtStatus})
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claims are unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items})
}

func registerNoShowAdminRoutes(g *gin.RouterGroup, a *adminAPI) {
	g.GET("/no-show/policies", a.listNoShowPolicies)
	g.POST("/no-show/policies", a.requireCSRF, a.createNoShowPolicy)
	g.POST("/no-show/policies/:id/approve", a.requireCSRF, a.approveNoShowPolicy)
	g.POST("/no-show/policies/:id/retirement-requests", a.requireCSRF, a.requestNoShowPolicyRetirement)
	g.POST("/no-show/policies/:id/retirement-requests/:requestID/approve", a.requireCSRF, a.approveNoShowPolicyRetirement)
	g.GET("/no-show/claims", a.listNoShowClaims)
	g.POST("/no-show/claims/:id/review", a.requireCSRF, a.reviewNoShowClaim)
}

func noShowAdminID(ctx *gin.Context) (string, bool) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_id", "Valid ID is required")
		return "", false
	}
	return id.String(), true
}

func (a *adminAPI) listNoShowPolicies(ctx *gin.Context) {
	market := strings.ToUpper(strings.TrimSpace(ctx.Query("marketCode")))
	status := ctx.DefaultQuery("status", "all")
	if status != "all" && status != "draft" && status != "approved" || market != "" && !geofenceCodePattern.MatchString(market) {
		driverError(ctx, http.StatusBadRequest, "invalid_filter", "No-show policy filter is invalid")
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,market_code,version,wait_minutes,fee_kobo,status,effective_from,effective_until,created_by::TEXT,approved_by::TEXT,created_at
		FROM no_show_policies WHERE ($1='' OR market_code=$1) AND ($2='all' OR status=$2) ORDER BY created_at DESC,id DESC LIMIT 100`, market, status)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policies are unavailable")
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var id, code, state, creator string
		var version, wait int
		var fee int64
		var from, created time.Time
		var until *time.Time
		var approver *string
		if rows.Scan(&id, &code, &version, &wait, &fee, &state, &from, &until, &creator, &approver, &created) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policies are unavailable")
			return
		}
		items = append(items, gin.H{"id": id, "marketCode": code, "version": version, "waitMinutes": wait, "feeKobo": fee, "currency": "NGN", "status": state, "effectiveFrom": from, "effectiveUntil": until, "createdBy": creator, "approvedBy": approver, "createdAt": created})
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policies are unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items})
}

func (a *adminAPI) createNoShowPolicy(ctx *gin.Context) {
	var b struct {
		MarketCode     string     `json:"marketCode"`
		WaitMinutes    *int       `json:"waitMinutes"`
		FeeKobo        *int64     `json:"feeKobo"`
		EffectiveFrom  time.Time  `json:"effectiveFrom"`
		EffectiveUntil *time.Time `json:"effectiveUntil"`
	}
	if ctx.ShouldBindJSON(&b) != nil || !geofenceCodePattern.MatchString(strings.ToUpper(strings.TrimSpace(b.MarketCode))) || b.WaitMinutes == nil || *b.WaitMinutes < 1 || *b.WaitMinutes > 180 || b.FeeKobo == nil || *b.FeeKobo <= 0 || b.EffectiveFrom.IsZero() || b.EffectiveUntil != nil && !b.EffectiveUntil.After(b.EffectiveFrom) {
		driverError(ctx, http.StatusBadRequest, "invalid_no_show_policy", "Market, wait period, positive fee, and valid effective dates are required")
		return
	}
	market := strings.ToUpper(strings.TrimSpace(b.MarketCode))
	id := uuid.NewString()
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	if _, err := tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,21))`, market); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be saved")
		return
	}
	var version int
	if err = tx.QueryRow(ctx.Request.Context(), `SELECT COALESCE(MAX(version),0)+1 FROM no_show_policies WHERE market_code=$1`, market).Scan(&version); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be saved")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO no_show_policies(id,market_code,version,wait_minutes,fee_kobo,status,effective_from,effective_until,created_by)
		VALUES($1::UUID,$2,$3,$4,$5,'draft',$6,$7,$8::UUID)`, id, market, version, *b.WaitMinutes, *b.FeeKobo, b.EffectiveFrom, b.EffectiveUntil, currentAdmin(ctx).ID)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be saved")
		return
	}
	if err = adminAudit(ctx, tx, "no_show_policy_created", "", gin.H{"policyId": id, "marketCode": market, "version": version, "waitMinutes": *b.WaitMinutes, "feeKobo": *b.FeeKobo, "effectiveFrom": b.EffectiveFrom, "effectiveUntil": b.EffectiveUntil}); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be saved")
		return
	}
	if err = tx.Commit(ctx.Request.Context()); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be saved")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id, "marketCode": market, "version": version, "waitMinutes": *b.WaitMinutes, "feeKobo": *b.FeeKobo, "currency": "NGN", "status": "draft", "effectiveFrom": b.EffectiveFrom, "effectiveUntil": b.EffectiveUntil}})
}

func (a *adminAPI) approveNoShowPolicy(ctx *gin.Context) {
	id, ok := noShowAdminID(ctx)
	if !ok {
		return
	}
	dbctx := ctx.Request.Context()
	tx, err := a.pool.Begin(dbctx)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be approved")
		return
	}
	defer tx.Rollback(dbctx)
	var market, state, creator string
	var from time.Time
	var until *time.Time
	err = tx.QueryRow(dbctx, `SELECT market_code,status,created_by::TEXT,effective_from,effective_until FROM no_show_policies WHERE id=$1::UUID FOR UPDATE`, id).Scan(&market, &state, &creator, &from, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "no_show_policy_not_found", "No-show policy was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be approved")
		return
	}
	adminID := currentAdmin(ctx).ID
	if creator == adminID {
		driverError(ctx, http.StatusConflict, "two_person_approval_required", "A different staff account must approve this policy")
		return
	}
	if state != "draft" {
		driverError(ctx, http.StatusConflict, "no_show_policy_not_draft", "Only draft policies can be approved")
		return
	}
	if !from.After(time.Now()) && until != nil && !until.After(time.Now()) {
		driverError(ctx, http.StatusConflict, "no_show_policy_expired", "Expired policy cannot be approved")
		return
	}
	if _, err := tx.Exec(dbctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,21))`, market); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be approved")
		return
	}
	var overlap bool
	err = tx.QueryRow(dbctx, `SELECT EXISTS(SELECT 1 FROM no_show_policies p WHERE p.id<>$1::UUID AND p.market_code=$2 AND p.status='approved'
	AND p.effective_from<COALESCE($4::TIMESTAMPTZ,'infinity'::TIMESTAMPTZ) AND COALESCE(p.effective_until,'infinity'::TIMESTAMPTZ)>$3::TIMESTAMPTZ)`, id, market, from, until).Scan(&overlap)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be approved")
		return
	}
	if overlap {
		driverError(ctx, http.StatusConflict, "no_show_policy_overlap", "Approved policy dates overlap another policy for this market")
		return
	}
	_, err = tx.Exec(dbctx, `UPDATE no_show_policies SET status='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1::UUID`, id, adminID)
	if err == nil {
		err = adminAudit(ctx, tx, "no_show_policy_approved", "", gin.H{"policyId": id, "marketCode": market})
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be approved")
		return
	}
	if err = tx.Commit(dbctx); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show policy could not be approved")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "status": "approved", "approvedBy": adminID}})
}

func (a *adminAPI) requestNoShowPolicyRetirement(ctx *gin.Context) {
	id, ok := noShowAdminID(ctx)
	if !ok {
		return
	}
	var b struct {
		EffectiveUntil time.Time `json:"effectiveUntil"`
	}
	if ctx.ShouldBindJSON(&b) != nil || !b.EffectiveUntil.After(time.Now()) {
		driverError(ctx, http.StatusBadRequest, "invalid_retirement", "A future effectiveUntil is required")
		return
	}
	dbctx := ctx.Request.Context()
	tx, err := a.pool.Begin(dbctx)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(dbctx)
	requestID := uuid.NewString()
	tag, err := tx.Exec(dbctx, `INSERT INTO no_show_policy_retirements(id,policy_id,effective_until,requested_by,status)
		SELECT $1::UUID,id,$3,$4::UUID,'pending' FROM no_show_policies WHERE id=$2::UUID AND status='approved' AND effective_until IS NULL AND effective_from<$3`, requestID, id, b.EffectiveUntil.UTC(), currentAdmin(ctx).ID)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(ctx, http.StatusConflict, "retirement_unavailable", "Policy cannot be retired")
		return
	}
	if err = adminAudit(ctx, tx, "no_show_policy_retirement_requested", "", gin.H{"policyId": id, "requestId": requestID, "effectiveUntil": b.EffectiveUntil.UTC()}); err != nil {
		reviewFailure(ctx)
		return
	}
	if err = tx.Commit(dbctx); err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": requestID, "status": "pending"}})
}

func (a *adminAPI) approveNoShowPolicyRetirement(ctx *gin.Context) {
	id, ok := noShowAdminID(ctx)
	if !ok {
		return
	}
	requestID, err := uuid.Parse(ctx.Param("requestID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_id", "Valid request ID is required")
		return
	}
	dbctx := ctx.Request.Context()
	tx, err := a.pool.Begin(dbctx)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(dbctx)
	var requester string
	var until time.Time
	err = tx.QueryRow(dbctx, `SELECT requested_by::TEXT,effective_until FROM no_show_policy_retirements WHERE id=$1::UUID AND policy_id=$2::UUID AND status='pending' FOR UPDATE`, requestID.String(), id).Scan(&requester, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "retirement_not_found", "Pending retirement was not found")
		return
	}
	if err != nil {
		reviewFailure(ctx)
		return
	}
	if requester == currentAdmin(ctx).ID || !until.After(time.Now()) {
		driverError(ctx, http.StatusConflict, "retirement_approval_unavailable", "A different staff member must approve before the retirement time")
		return
	}
	tag, err := tx.Exec(dbctx, `UPDATE no_show_policies SET effective_until=$2 WHERE id=$1::UUID AND status='approved' AND effective_until IS NULL`, id, until)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(ctx, http.StatusConflict, "retirement_unavailable", "Policy cannot be retired")
		return
	}
	_, err = tx.Exec(dbctx, `UPDATE no_show_policy_retirements SET status='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1::UUID`, requestID.String(), currentAdmin(ctx).ID)
	if err != nil || adminAudit(ctx, tx, "no_show_policy_retirement_approved", "", gin.H{"policyId": id, "requestId": requestID.String(), "effectiveUntil": until}) != nil {
		reviewFailure(ctx)
		return
	}
	if err = tx.Commit(dbctx); err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "effectiveUntil": until}})
}

func (a *adminAPI) listNoShowClaims(ctx *gin.Context) {
	filter := ctx.DefaultQuery("status", "pending")
	if filter != "all" && filter != "pending" && filter != "approved" && filter != "rejected" && filter != "settled" {
		driverError(ctx, http.StatusBadRequest, "invalid_status", "Claim status is invalid")
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT c.id::TEXT,c.trip_id::TEXT,c.driver_id::TEXT,c.rider_id::TEXT,c.market_code,c.fee_kobo,c.arrived_at,c.eligible_at,c.status,c.submitted_at,c.reviewed_at,c.review_note
		FROM driver_no_show_claims c WHERE ($1='all' OR c.status=$1) ORDER BY c.submitted_at ASC,c.id LIMIT 100`, filter)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claims are unavailable")
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var id, trip, driver, rider, market, status string
		var fee int64
		var arrived, eligible, submitted time.Time
		var reviewed *time.Time
		var note *string
		if rows.Scan(&id, &trip, &driver, &rider, &market, &fee, &arrived, &eligible, &status, &submitted, &reviewed, &note) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claims are unavailable")
			return
		}
		items = append(items, gin.H{"id": id, "tripId": trip, "driverId": driver, "riderId": rider, "marketCode": market, "feeKobo": fee, "currency": "NGN", "arrivedAt": arrived, "eligibleAt": eligible, "status": status, "submittedAt": submitted, "reviewedAt": reviewed, "reviewNote": note})
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "No-show claims are unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items})
}

func (a *adminAPI) reviewNoShowClaim(ctx *gin.Context) {
	id, ok := noShowAdminID(ctx)
	if !ok {
		return
	}
	var b struct {
		Outcome string `json:"outcome"`
		Note    string `json:"note"`
	}
	if ctx.ShouldBindJSON(&b) != nil || b.Outcome != "approved" && b.Outcome != "rejected" || len(b.Note) > 1000 {
		driverError(ctx, http.StatusBadRequest, "invalid_claim_review", "Outcome must be approved or rejected and note must be at most 1000 characters")
		return
	}
	dbctx := ctx.Request.Context()
	tx, err := a.pool.Begin(dbctx)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "Claim review could not be saved")
		return
	}
	defer tx.Rollback(dbctx)
	var trip, driver, rider, status string
	var fee int64
	var market string
	err = tx.QueryRow(dbctx, `SELECT trip_id::TEXT,driver_id::TEXT,rider_id::TEXT,status,fee_kobo,market_code FROM driver_no_show_claims WHERE id=$1::UUID FOR UPDATE`, id).Scan(&trip, &driver, &rider, &status, &fee, &market)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "no_show_claim_not_found", "No-show claim was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "Claim review could not be saved")
		return
	}
	if status != "pending" {
		driverError(ctx, http.StatusConflict, "no_show_claim_resolved", "Claim has already been reviewed")
		return
	}
	adminID := currentAdmin(ctx).ID
	_, err = tx.Exec(dbctx, `UPDATE driver_no_show_claims SET status=$2,reviewed_by=$3::UUID,reviewed_at=NOW(),review_note=NULLIF($4,'') WHERE id=$1::UUID`, id, b.Outcome, adminID, strings.TrimSpace(b.Note))
	if err == nil {
		_, err = tx.Exec(dbctx, `INSERT INTO no_show_claim_reviews(id,claim_id,admin_id,outcome,note) VALUES($1::UUID,$2::UUID,$3::UUID,$4,$5)`, uuid.NewString(), id, adminID, b.Outcome, strings.TrimSpace(b.Note))
	}
	if err == nil && b.Outcome == "approved" {
		_, err = tx.Exec(dbctx, `INSERT INTO rider_no_show_debts(id,claim_id,rider_id,beneficiary_driver_id,amount_kobo,status) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,$5,'due')`, uuid.NewString(), id, rider, driver, fee)
	}
	if err == nil {
		err = adminAudit(ctx, tx, "no_show_claim_"+b.Outcome, driver, gin.H{"claimId": id, "tripId": trip, "riderId": rider, "feeKobo": fee, "note": strings.TrimSpace(b.Note)})
	}
	if err == nil {
		_, err = insertDriverEvent(ctx, tx, driver, contracts.TripEventNoShowClaimUpdated, gin.H{"tripId": trip, "claimId": id, "status": b.Outcome, "feeKobo": fee, "currency": "NGN", "reviewNote": strings.TrimSpace(b.Note)})
	}
	if err == nil {
		_, err = insertDriverEvent(ctx, tx, rider, contracts.TripEventNoShowClaimUpdated, gin.H{"tripId": trip, "claimId": id, "status": b.Outcome, "feeKobo": fee, "currency": "NGN", "reviewNote": strings.TrimSpace(b.Note)})
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "Claim review could not be saved")
		return
	}
	if err = tx.Commit(dbctx); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "Claim review could not be saved")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "tripId": trip, "status": b.Outcome, "feeKobo": fee, "currency": "NGN", "reviewedBy": adminID, "reviewNote": strings.TrimSpace(b.Note)}})
}

type noShowDebtTransfer struct {
	debtID, claimID, beneficiary string
	amount                       int64
}

// settleNoShowDebtTransfers books the cash part of a confirmed direct rider
// payment as a balanced transfer between the collecting driver's and the
// claiming driver's Operating Balances. The caller owns the settlement tx.
func settleNoShowDebtTransfers(ctx *gin.Context, tx pgx.Tx, tripID, collectorID string) error {
	dbctx := ctx.Request.Context()
	rows, err := tx.Query(dbctx, `SELECT s.debt_id::TEXT,d.claim_id::TEXT,s.beneficiary_driver_id::TEXT,s.amount_kobo
		FROM trip_no_show_debt_settlements s JOIN rider_no_show_debts d ON d.id=s.debt_id
		WHERE s.trip_id=$1::UUID AND s.status='pending' AND d.status='due' ORDER BY s.debt_id FOR UPDATE OF s,d`, tripID)
	if err != nil {
		return err
	}
	transfers := []noShowDebtTransfer{}
	var amountTotal int64
	for rows.Next() {
		var t noShowDebtTransfer
		if err := rows.Scan(&t.debtID, &t.claimID, &t.beneficiary, &t.amount); err != nil {
			rows.Close()
			return err
		}
		transfers = append(transfers, t)
		amountTotal += t.amount
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var expected int64
	if err := tx.QueryRow(dbctx, `SELECT no_show_debt_kobo FROM trip_settlements WHERE trip_id=$1::UUID`, tripID).Scan(&expected); err != nil {
		return err
	}
	if amountTotal != expected {
		return errors.New("no-show debt settlement allocation does not match settlement amount")
	}
	if amountTotal == 0 {
		return nil
	}
	ids := map[string]bool{collectorID: true}
	for _, t := range transfers {
		ids[t.beneficiary] = true
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	balances := map[string]int64{}
	for _, id := range ordered {
		if _, err := tx.Exec(dbctx, `INSERT INTO driver_operating_accounts(driver_id,balance_kobo) VALUES($1::UUID,0) ON CONFLICT(driver_id) DO NOTHING`, id); err != nil {
			return err
		}
		var balance int64
		if err := tx.QueryRow(dbctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID FOR UPDATE`, id).Scan(&balance); err != nil {
			return err
		}
		balances[id] = balance
	}
	var allowNegative bool
	var maxNegative int64
	err = tx.QueryRow(dbctx, `SELECT COALESCE((SELECT p.allow_negative FROM drivers d JOIN operating_balance_policies p ON p.market_code=d.online_market_code
		WHERE d.id=$1::UUID AND p.status='approved' AND p.effective_from<=NOW() AND (p.effective_until IS NULL OR p.effective_until>NOW()) ORDER BY p.version DESC LIMIT 1),FALSE),
		COALESCE((SELECT p.maximum_negative_kobo FROM drivers d JOIN operating_balance_policies p ON p.market_code=d.online_market_code
		WHERE d.id=$1::UUID AND p.status='approved' AND p.effective_from<=NOW() AND (p.effective_until IS NULL OR p.effective_until>NOW()) ORDER BY p.version DESC LIMIT 1),0)`, collectorID).Scan(&allowNegative, &maxNegative)
	if err != nil {
		return err
	}
	minimum := int64(0)
	if allowNegative {
		minimum = -maxNegative
	}
	for _, transfer := range transfers {
		fromDelta, toDelta := -transfer.amount, transfer.amount
		if collectorID == transfer.beneficiary {
			fromDelta = 0
			toDelta = transfer.amount
		}
		fromAfter := balances[collectorID] + fromDelta
		toAfter := balances[transfer.beneficiary] + toDelta
		if fromAfter < minimum {
			if _, err := tx.Exec(dbctx, `UPDATE rider_no_show_debts SET status='collection_pending' WHERE id=$1::UUID AND status='due'`, transfer.debtID); err != nil {
				return err
			}
			if _, err := tx.Exec(dbctx, `UPDATE trip_no_show_debt_settlements SET status='collection_pending' WHERE trip_id=$1::UUID AND debt_id=$2::UUID AND status='pending'`, tripID, transfer.debtID); err != nil {
				return err
			}
			pendingPayload := gin.H{"tripId": tripID, "claimId": transfer.claimID, "status": "collection_pending", "feeKobo": transfer.amount, "currency": "NGN"}
			if _, err := insertDriverEvent(ctx, tx, transfer.beneficiary, contracts.TripEventNoShowClaimUpdated, pendingPayload); err != nil {
				return err
			}
			if _, err := insertDriverEvent(ctx, tx, collectorID, contracts.TripEventNoShowClaimUpdated, pendingPayload); err != nil {
				return err
			}
			var riderID string
			if err := tx.QueryRow(dbctx, `SELECT rider_id::TEXT FROM rider_no_show_debts WHERE id=$1::UUID`, transfer.debtID).Scan(&riderID); err != nil {
				return err
			}
			if _, err := insertDriverEvent(ctx, tx, riderID, contracts.TripEventNoShowClaimUpdated, pendingPayload); err != nil {
				return err
			}
			continue
		}
		var fromEntry, toEntry any
		transferID := uuid.NewString()
		if fromDelta != 0 {
			entryID := uuid.NewString()
			details := gin.H{"transferId": transferID, "debtId": transfer.debtID, "tripId": tripID, "role": "source"}
			if _, err := tx.Exec(dbctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details)
				VALUES($1::UUID,$2::UUID,'no_show_transfer',$3,$4,$5,$6::JSONB)`, entryID, collectorID, fromDelta, fromAfter, "no-show:"+transfer.debtID+":source", details); err != nil {
				return err
			}
			fromEntry = entryID
			balances[collectorID] = fromAfter
		}
		entryID := uuid.NewString()
		details := gin.H{"transferId": transferID, "debtId": transfer.debtID, "tripId": tripID, "role": "beneficiary"}
		if _, err := tx.Exec(dbctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details)
			VALUES($1::UUID,$2::UUID,'no_show_transfer',$3,$4,$5,$6::JSONB)`, entryID, transfer.beneficiary, toDelta, toAfter, "no-show:"+transfer.debtID+":beneficiary", details); err != nil {
			return err
		}
		toEntry = entryID
		balances[transfer.beneficiary] = toAfter
		if _, err := tx.Exec(dbctx, `UPDATE driver_operating_accounts SET balance_kobo=$2,updated_at=NOW() WHERE driver_id=$1::UUID`, collectorID, balances[collectorID]); err != nil {
			return err
		}
		if transfer.beneficiary != collectorID {
			if _, err := tx.Exec(dbctx, `UPDATE driver_operating_accounts SET balance_kobo=$2,updated_at=NOW() WHERE driver_id=$1::UUID`, transfer.beneficiary, balances[transfer.beneficiary]); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(dbctx, `INSERT INTO operating_balance_transfers(id,debt_id,trip_id,from_driver_id,to_driver_id,amount_kobo,from_entry_id,to_entry_id)
			VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,$5::UUID,$6,$7::UUID,$8::UUID)`, transferID, transfer.debtID, tripID, collectorID, transfer.beneficiary, transfer.amount, fromEntry, toEntry); err != nil {
			return err
		}
		if _, err := tx.Exec(dbctx, `UPDATE rider_no_show_debts SET status='settled',settled_at=NOW() WHERE id=$1::UUID AND status='due'`, transfer.debtID); err != nil {
			return err
		}
		if _, err := tx.Exec(dbctx, `UPDATE driver_no_show_claims SET status='settled' WHERE id=$1::UUID AND status='approved'`, transfer.claimID); err != nil {
			return err
		}
		if _, err := tx.Exec(dbctx, `UPDATE trip_no_show_debt_settlements SET status='settled',settled_at=NOW() WHERE trip_id=$1::UUID AND debt_id=$2::UUID AND status='pending'`, tripID, transfer.debtID); err != nil {
			return err
		}
		payload := gin.H{"tripId": tripID, "claimId": transfer.claimID, "status": "settled", "feeKobo": transfer.amount, "currency": "NGN"}
		if _, err := insertDriverEvent(ctx, tx, transfer.beneficiary, contracts.TripEventNoShowClaimUpdated, payload); err != nil {
			return err
		}
		var riderID string
		if err := tx.QueryRow(dbctx, `SELECT rider_id::TEXT FROM rider_no_show_debts WHERE id=$1::UUID`, transfer.debtID).Scan(&riderID); err != nil {
			return err
		}
		if _, err := insertDriverEvent(ctx, tx, riderID, contracts.TripEventNoShowClaimUpdated, payload); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(dbctx, `UPDATE drivers SET status=CASE WHEN online_requested AND driver_balance_eligible(id,online_market_code)
		AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE) THEN 'available' ELSE 'offline' END,
		available=online_requested AND driver_balance_eligible(id,online_market_code)
		AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE),updated_at=NOW() WHERE id=$1::UUID`, collectorID); err != nil {
		return err
	}
	return nil
}
