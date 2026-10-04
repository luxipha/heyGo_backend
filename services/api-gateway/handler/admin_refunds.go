package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/env"
)

func (a *adminAPI) createRefundRequest(ctx *gin.Context) {
	var body struct {
		TopupID    string `json:"topupId"`
		CaseID     string `json:"caseId"`
		ReasonCode string `json:"reasonCode"`
		Reason     string `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_refund", "Top-up, case, and reason are required")
		return
	}
	topupID, topupErr := uuid.Parse(body.TopupID)
	caseID, caseErr := uuid.Parse(body.CaseID)
	reason := strings.TrimSpace(body.Reason)
	if topupErr != nil || caseErr != nil || !refundReasonCodePattern.MatchString(body.ReasonCode) || len([]rune(reason)) < 5 || len([]rune(reason)) > 1000 {
		driverError(ctx, http.StatusBadRequest, "invalid_refund", "Valid Top-up, case, reason code, and reason are required")
		return
	}
	var driverID, paymentRef, transactionRef, status string
	var amount int64
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT driver_id::TEXT,provider_reference,provider_transaction_reference,amount_kobo,status
		FROM driver_operating_topups WHERE id=$1 AND provider='monnify'`, topupID).Scan(&driverID, &paymentRef, &transactionRef, &amount, &status)
	if errors.Is(err, pgx.ErrNoRows) || status != "succeeded" || transactionRef == "" {
		driverError(ctx, http.StatusConflict, "topup_not_refundable", "A successful Monnify Driver top-up is required")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund source is unavailable")
		return
	}
	// Verify the original provider transaction before creating any money request.
	verified, err := verifyRefundSource(ctx.Request.Context(), transactionRef)
	if err != nil || verified.Data.TransactionReference != transactionRef || verified.Data.PaymentReference != paymentRef || verified.Data.AmountKobo != amount || !strings.EqualFold(verified.Data.PaymentMethod, "ACCOUNT_TRANSFER") {
		driverError(ctx, http.StatusConflict, "topup_provider_unverified", "Original provider payment could not be verified for refund")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund request could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var policyID string
	var maxRequest int64
	var reasonCodes []string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,max_request_kobo,reason_codes FROM refund_policy_versions
		WHERE state='approved' AND effective_from<=NOW() AND (effective_to IS NULL OR effective_to>NOW())
		ORDER BY version DESC LIMIT 1`).Scan(&policyID, &maxRequest, &reasonCodes)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusConflict, "refund_policy_inactive", "An approved active refund policy is required")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund policy is unavailable")
		return
	}
	allowed := false
	for _, code := range reasonCodes {
		allowed = allowed || code == body.ReasonCode
	}
	if !allowed || amount < 10000 || amount > maxRequest {
		driverError(ctx, http.StatusConflict, "refund_outside_policy", "Top-up amount or reason is outside the active policy")
		return
	}
	var caseValid bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM support_cases WHERE id=$1 AND opened_by=$2::UUID
		AND audience='driver' AND topic_code='payment')`, caseID, driverID).Scan(&caseValid)
	if err != nil || !caseValid {
		driverError(ctx, http.StatusConflict, "refund_case_invalid", "A linked Driver payment case is required")
		return
	}
	id := uuid.New()
	ref := "HEYGO-REFUND-" + id.String()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO refund_requests(id,topup_id,driver_id,case_id,policy_id,amount_kobo,reason_code,reason,proposed_by,refund_reference)
		VALUES($1,$2,$3::UUID,$4,$5::UUID,$6,$7,$8,$9::UUID,$10)`, id, topupID, driverID, caseID, policyID, amount, body.ReasonCode, reason, currentAdmin(ctx).ID, ref)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO refund_actions(id,refund_id,actor_admin_id,action,reason,after_state)
			VALUES($1,$2,$3::UUID,'proposed',$4,'draft')`, uuid.New(), id, currentAdmin(ctx).ID, reason)
	}
	if err == nil {
		err = adminAudit(ctx, tx, "finance.refund.proposed", "", gin.H{"refundId": id.String(), "topupId": topupID.String(), "caseId": caseID.String(), "amountKobo": amount, "reasonCode": body.ReasonCode, "reason": reason})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusConflict, "refund_request_conflict", "A refund for this top-up may already exist")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id.String(), "state": "draft", "topupId": topupID.String(), "amountKobo": amount}})
}

func (a *adminAPI) approveRefundRequest(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_refund_id", "Valid refund ID is required")
		return
	}
	if !env.GetBool("HEYGO_MONNIFY_REFUNDS_READY", false) {
		driverError(ctx, http.StatusConflict, "refund_provider_not_ready", "Refund execution is awaiting verified provider setup")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund could not be approved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var driverID, proposer, state, topupID, nameRecordID string
	var amount, maxDaily int64
	var nameMatched bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT r.driver_id::TEXT,r.proposed_by::TEXT,r.state,r.topup_id::TEXT,r.amount_kobo,p.max_daily_kobo,
		COALESCE(d.name_matched,FALSE),COALESCE(d.name_record_id::TEXT,'')
		FROM refund_requests r JOIN refund_policy_versions p ON p.id=r.policy_id LEFT JOIN refund_destinations d ON d.refund_id=r.id
		WHERE r.id=$1 FOR UPDATE OF r`, id).Scan(&driverID, &proposer, &state, &topupID, &amount, &maxDaily, &nameMatched, &nameRecordID)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "refund_not_found", "Refund request was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund could not be approved")
		return
	}
	if state != "draft" || proposer == currentAdmin(ctx).ID || !nameMatched || nameRecordID == "" {
		driverError(ctx, http.StatusConflict, "refund_approval_conflict", "A different Finance approver and verified Driver-owned destination are required")
		return
	}
	var identityValid bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_verified_legal_names n
		JOIN driver_profiles p ON p.driver_id=n.driver_id JOIN driver_documents doc ON doc.id=n.evidence_document_id
		WHERE n.id=$1::UUID AND n.driver_id=$2::UUID AND p.manual_verification_status='approved'
		AND doc.status='approved' AND (doc.expires_at IS NULL OR doc.expires_at>NOW())
		AND n.id=(SELECT id FROM driver_verified_legal_names WHERE driver_id=$2::UUID ORDER BY created_at DESC,id DESC LIMIT 1))`, nameRecordID, driverID).Scan(&identityValid)
	if err != nil || !identityValid {
		driverError(ctx, http.StatusConflict, "refund_identity_recheck_required", "Compliance identity evidence must be current")
		return
	}
	// The account row lock serializes all Driver balance writers; the database
	// trigger rejects a competing debit that would consume this reservation.
	var balance, reserved int64
	err = tx.QueryRow(ctx.Request.Context(), `SELECT balance_kobo,refund_reserved_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID FOR UPDATE`, driverID).Scan(&balance, &reserved)
	if err != nil || balance-reserved < amount {
		driverError(ctx, http.StatusConflict, "refund_balance_unavailable", "Available Operating Balance cannot cover the refund")
		return
	}
	// A single advisory lock protects the policy-wide daily cap. Nigeria local
	// calendar days are authoritative for this launch policy.
	_, err = tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('refund-daily-'||(NOW() AT TIME ZONE 'Africa/Lagos')::DATE::TEXT,0))`)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund daily limit could not be checked")
		return
	}
	var approvedToday int64
	err = tx.QueryRow(ctx.Request.Context(), `SELECT COALESCE(SUM(amount_kobo),0) FROM refund_requests
		WHERE state IN ('approved','provider_pending','needs_reconciliation','completed')
		AND (approved_at AT TIME ZONE 'Africa/Lagos')::DATE=(NOW() AT TIME ZONE 'Africa/Lagos')::DATE`).Scan(&approvedToday)
	if err != nil || approvedToday > maxDaily-amount {
		driverError(ctx, http.StatusConflict, "refund_daily_limit", "Nigeria-day refund limit would be exceeded")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE driver_operating_accounts SET refund_reserved_kobo=refund_reserved_kobo+$2,updated_at=NOW() WHERE driver_id=$1::UUID`, driverID, amount)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `UPDATE refund_requests SET state='approved',approved_by=$2::UUID,approved_at=NOW(),updated_at=NOW() WHERE id=$1`, id, currentAdmin(ctx).ID)
	}
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO refund_actions(id,refund_id,actor_admin_id,action,reason,before_state,after_state)
			VALUES($1,$2,$3::UUID,'approved','Approved verified full top-up refund','draft','approved')`, uuid.New(), id, currentAdmin(ctx).ID)
	}
	if err == nil {
		err = adminAudit(ctx, tx, "finance.refund.approved", "", gin.H{"refundId": id.String(), "topupId": topupID, "amountKobo": amount, "beforeState": "draft", "afterState": "approved"})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund could not be approved")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id.String(), "state": "approved", "amountKobo": amount}})
}

func (a *adminAPI) refundRequests(ctx *gin.Context) {
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT r.id::TEXT,r.created_at,r.driver_id::TEXT,r.topup_id::TEXT,r.case_id::TEXT,
		r.amount_kobo,r.reason_code,r.state,d.account_number_masked,d.name_matched
		FROM refund_requests r LEFT JOIN refund_destinations d ON d.refund_id=r.id
		WHERE ($1::TIMESTAMPTZ IS NULL OR (r.created_at,r.id)<($1,$2::UUID))
		ORDER BY r.created_at DESC,r.id DESC LIMIT $3`, at, id, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund requests are unavailable")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit+1)
	times := make([]time.Time, 0, limit+1)
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id, driver, topup, caseID, reasonCode, state string
		var created time.Time
		var amount int64
		var masked *string
		var matched *bool
		if rows.Scan(&id, &created, &driver, &topup, &caseID, &amount, &reasonCode, &state, &masked, &matched) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund requests are unavailable")
			return
		}
		items = append(items, gin.H{"id": id, "driverId": driver, "topupId": topup, "caseId": caseID, "amountKobo": amount,
			"reasonCode": reasonCode, "state": state, "maskedDestination": masked, "nameMatched": matched, "createdAt": created})
		times = append(times, created)
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund requests are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: times[limit-1], ID: ids[limit-1]})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) refundRequest(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_refund_id", "Valid refund ID is required")
		return
	}
	var driverID, topupID, caseID, policyID, reasonCode, reason, state, reference, proposer string
	var amount int64
	var approver, masked, providerStatus, providerReference *string
	var nameMatched *bool
	var created time.Time
	var approvedAt, completedAt *time.Time
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT r.driver_id::TEXT,r.topup_id::TEXT,r.case_id::TEXT,r.policy_id::TEXT,
		r.amount_kobo,r.reason_code,r.reason,r.state,r.refund_reference,r.proposed_by::TEXT,r.approved_by::TEXT,
		d.account_number_masked,d.name_matched,r.provider_status,r.provider_transaction_reference,
		r.created_at,r.approved_at,r.completed_at
		FROM refund_requests r LEFT JOIN refund_destinations d ON d.refund_id=r.id WHERE r.id=$1`, id).
		Scan(&driverID, &topupID, &caseID, &policyID, &amount, &reasonCode, &reason, &state, &reference, &proposer, &approver,
			&masked, &nameMatched, &providerStatus, &providerReference, &created, &approvedAt, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "refund_not_found", "Refund request was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund request is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id.String(), "driverId": driverID, "topupId": topupID,
		"caseId": caseID, "policyId": policyID, "amountKobo": amount, "reasonCode": reasonCode, "reason": reason,
		"state": state, "refundReference": reference, "proposedBy": proposer, "approvedBy": approver,
		"maskedDestination": masked, "nameMatched": nameMatched, "providerStatus": providerStatus,
		"providerTransactionReference": providerReference, "createdAt": created, "approvedAt": approvedAt, "completedAt": completedAt}})
}
