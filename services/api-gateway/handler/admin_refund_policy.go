package handler

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

var refundReasonCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)

func registerAdminRefundRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/finance/refund-policy", a.refundPolicies)
	group.POST("/finance/refund-policy", a.requireCSRF, a.createRefundPolicy)
	group.POST("/finance/refund-policy/:id/approve", a.requireCSRF, a.approveRefundPolicy)
	group.POST("/finance/refund-policy/:id/retirement-requests", a.requireCSRF, a.requestRefundPolicyRetirement)
	group.POST("/finance/refund-policy/:id/retirement-requests/:requestID/approve", a.requireCSRF, a.approveRefundPolicyRetirement)
	group.GET("/finance/refunds", a.refundRequests)
	group.POST("/finance/refunds", a.requireCSRF, a.createRefundRequest)
	group.GET("/finance/refunds/:id", a.refundRequest)
	group.POST("/finance/refunds/:id/approve", a.requireCSRF, a.approveRefundRequest)
}

func (a *adminAPI) refundPolicies(ctx *gin.Context) {
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,version,reason_codes,max_request_kobo,max_daily_kobo,effective_from,effective_to,state,
		proposed_by::TEXT,approved_by::TEXT,created_at,approved_at FROM refund_policy_versions ORDER BY version DESC LIMIT 100`)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_policy_unavailable", "Refund policy is unavailable")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, state, proposed string
		var version int
		var reasons []string
		var maxRequest, maxDaily int64
		var from, created time.Time
		var to, approvedAt *time.Time
		var approvedBy *string
		if rows.Scan(&id, &version, &reasons, &maxRequest, &maxDaily, &from, &to, &state, &proposed, &approvedBy, &created, &approvedAt) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "refund_policy_unavailable", "Refund policy is unavailable")
			return
		}
		items = append(items, gin.H{"id": id, "version": version, "reasonCodes": reasons, "maxRequestKobo": maxRequest, "maxDailyKobo": maxDaily,
			"effectiveFrom": from, "effectiveTo": to, "state": state, "proposedBy": proposed, "approvedBy": approvedBy, "createdAt": created, "approvedAt": approvedAt})
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_policy_unavailable", "Refund policy is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items})
}

func (a *adminAPI) createRefundPolicy(ctx *gin.Context) {
	var body struct {
		ReasonCodes    []string   `json:"reasonCodes"`
		MaxRequestKobo int64      `json:"maxRequestKobo"`
		MaxDailyKobo   int64      `json:"maxDailyKobo"`
		EffectiveFrom  time.Time  `json:"effectiveFrom"`
		EffectiveTo    *time.Time `json:"effectiveTo"`
	}
	if ctx.ShouldBindJSON(&body) != nil || len(body.ReasonCodes) == 0 || len(body.ReasonCodes) > 20 ||
		body.MaxRequestKobo < 10000 || body.MaxDailyKobo < body.MaxRequestKobo || body.EffectiveFrom.IsZero() ||
		(body.EffectiveTo != nil && !body.EffectiveTo.After(body.EffectiveFrom)) {
		driverError(ctx, http.StatusBadRequest, "invalid_refund_policy", "Valid reason codes, kobo limits, and effective period are required")
		return
	}
	seen := make(map[string]bool)
	for i, raw := range body.ReasonCodes {
		code := strings.TrimSpace(raw)
		if !refundReasonCodePattern.MatchString(code) || seen[code] {
			driverError(ctx, http.StatusBadRequest, "invalid_refund_policy", "Reason codes must be unique lower-case identifiers")
			return
		}
		seen[code] = true
		body.ReasonCodes[i] = code
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_policy_unavailable", "Refund policy could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	// Serialize version assignment with approvals.
	_, err = tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(74381001)`)
	var version int
	if err == nil {
		err = tx.QueryRow(ctx.Request.Context(), `SELECT COALESCE(MAX(version),0)+1 FROM refund_policy_versions`).Scan(&version)
	}
	id := uuid.New()
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO refund_policy_versions(id,version,reason_codes,max_request_kobo,max_daily_kobo,effective_from,effective_to,proposed_by)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8::UUID)`, id, version, body.ReasonCodes, body.MaxRequestKobo, body.MaxDailyKobo, body.EffectiveFrom, body.EffectiveTo, currentAdmin(ctx).ID)
	}
	if err == nil {
		err = adminAudit(ctx, tx, "finance.refund_policy.proposed", "", gin.H{"policyId": id.String(), "version": version, "maxRequestKobo": body.MaxRequestKobo, "maxDailyKobo": body.MaxDailyKobo})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_policy_unavailable", "Refund policy could not be saved")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id.String(), "version": version, "state": "draft"}})
}

func (a *adminAPI) approveRefundPolicy(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_policy_id", "Valid policy ID is required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_policy_unavailable", "Refund policy could not be approved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	_, err = tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(74381001)`)
	var proposer, state string
	var from time.Time
	var to *time.Time
	if err == nil {
		err = tx.QueryRow(ctx.Request.Context(), `SELECT proposed_by::TEXT,state,effective_from,effective_to FROM refund_policy_versions WHERE id=$1 FOR UPDATE`, id).Scan(&proposer, &state, &from, &to)
	}
	if err == pgx.ErrNoRows {
		driverError(ctx, http.StatusNotFound, "refund_policy_not_found", "Refund policy was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_policy_unavailable", "Refund policy could not be approved")
		return
	}
	if state != "draft" || proposer == currentAdmin(ctx).ID {
		driverError(ctx, http.StatusConflict, "refund_policy_approval_conflict", "A different Finance staff account must approve a draft")
		return
	}
	var overlap bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM refund_policy_versions WHERE id<>$1 AND state='approved'
		AND tstzrange(effective_from,effective_to,'[)') && tstzrange($2,$3,'[)'))`, id, from, to).Scan(&overlap)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_policy_unavailable", "Refund policy could not be approved")
		return
	}
	if overlap {
		driverError(ctx, http.StatusConflict, "refund_policy_overlap", "Approved refund policy periods cannot overlap")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE refund_policy_versions SET state='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1`, id, currentAdmin(ctx).ID)
	if err == nil {
		err = adminAudit(ctx, tx, "finance.refund_policy.approved", "", gin.H{"policyId": id.String(), "state": "approved"})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_policy_unavailable", "Refund policy could not be approved")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id.String(), "state": "approved"}})
}

func (a *adminAPI) requestRefundPolicyRetirement(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_policy_id", "Valid policy ID is required")
		return
	}
	var body struct {
		EffectiveUntil time.Time `json:"effectiveUntil"`
	}
	if ctx.ShouldBindJSON(&body) != nil || !body.EffectiveUntil.After(time.Now()) {
		driverError(ctx, http.StatusBadRequest, "invalid_retirement", "A future effectiveUntil is required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	requestID := uuid.New()
	tag, err := tx.Exec(ctx.Request.Context(), `INSERT INTO refund_policy_retirements(id,policy_id,effective_until,requested_by,status)
		SELECT $1,id,$3,$4::UUID,'pending' FROM refund_policy_versions
		WHERE id=$2 AND state='approved' AND effective_to IS NULL AND effective_from<$3`, requestID, id, body.EffectiveUntil.UTC(), currentAdmin(ctx).ID)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(ctx, http.StatusConflict, "retirement_unavailable", "Refund policy cannot be retired")
		return
	}
	if adminAudit(ctx, tx, "finance.refund_policy.retirement.requested", "", gin.H{"policyId": id.String(), "requestId": requestID.String(), "effectiveUntil": body.EffectiveUntil.UTC()}) != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": requestID.String(), "status": "pending"}})
}

func (a *adminAPI) approveRefundPolicyRetirement(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_policy_id", "Valid policy ID is required")
		return
	}
	requestID, err := uuid.Parse(ctx.Param("requestID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_retirement_id", "Valid retirement ID is required")
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
	err = tx.QueryRow(ctx.Request.Context(), `SELECT requested_by::TEXT,effective_until FROM refund_policy_retirements
		WHERE id=$1 AND policy_id=$2 AND status='pending' FOR UPDATE`, requestID, id).Scan(&requester, &until)
	if err == pgx.ErrNoRows {
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
	if _, err = tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(74381001)`); err != nil {
		reviewFailure(ctx)
		return
	}
	tag, err := tx.Exec(ctx.Request.Context(), `UPDATE refund_policy_versions SET effective_to=$2 WHERE id=$1 AND state='approved' AND effective_to IS NULL`, id, until)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(ctx, http.StatusConflict, "retirement_unavailable", "Refund policy cannot be retired")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE refund_policy_retirements SET status='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1`, requestID, currentAdmin(ctx).ID)
	if err != nil || adminAudit(ctx, tx, "finance.refund_policy.retirement.approved", "", gin.H{"policyId": id.String(), "requestId": requestID.String(), "effectiveUntil": until}) != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": requestID.String(), "policyId": id.String(), "status": "approved", "effectiveUntil": until, "proposedBy": requester, "approvedBy": currentAdmin(ctx).ID}})
}
