package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func registerAdminFinanceContextRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/finance/topups", a.financeTopups)
	group.GET("/finance/charges", a.financeCharges)
	group.GET("/finance/arrears", a.financeArrears)
	group.GET("/finance/settlements", a.financeSettlements)
	group.GET("/finance/debt-payments", a.financeDebtPayments)
	group.GET("/finance/payments", a.financePayments)
	group.GET("/finance/handoffs", a.financeHandoffs)
	registerAdminRefundRoutes(group, a)
}

func financeDriverPage(ctx *gin.Context) (uuid.UUID, int, any, any, bool) {
	driverID, err := uuid.Parse(ctx.Query("driverId"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_driver_id", "Valid driverId filter is required")
		return uuid.Nil, 0, nil, nil, false
	}
	limit, at, id, ok := staffPage(ctx)
	return driverID, limit, at, id, ok
}

func (a *adminAPI) financeJSONPage(ctx *gin.Context, query string) {
	driverID, limit, at, id, ok := financeDriverPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), query, driverID, at, id, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Finance records are unavailable")
		return
	}
	defer rows.Close()
	items := make([]json.RawMessage, 0, limit+1)
	times := make([]time.Time, 0, limit+1)
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var rowID string
		var created time.Time
		var payload []byte
		if rows.Scan(&rowID, &created, &payload) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Finance records are unavailable")
			return
		}
		items = append(items, json.RawMessage(payload))
		ids = append(ids, rowID)
		times = append(times, created)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Finance records are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		value := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: times[limit-1], ID: ids[limit-1]})
		next = &value
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) financeTopups(ctx *gin.Context) {
	a.financeJSONPage(ctx, `SELECT t.id::TEXT,t.created_at,jsonb_build_object(
		'id',t.id,'driverId',t.driver_id,'amountKobo',t.amount_kobo,'status',t.status,
		'provider',t.provider,'paymentReference',t.provider_reference,
		'providerTransactionReference',t.provider_transaction_reference,
		'marketCode',t.market_code,'policyId',t.policy_id,'confirmedAt',t.confirmed_at,
		'operatingEntryId',e.id,'createdAt',t.created_at)
		FROM driver_operating_topups t LEFT JOIN driver_operating_entries e ON e.details->>'topupId'=t.id::TEXT
		WHERE t.driver_id=$1 AND ($2::TIMESTAMPTZ IS NULL OR (t.created_at,t.id)<($2,$3::UUID))
		ORDER BY t.created_at DESC,t.id DESC LIMIT $4`)
}

func (a *adminAPI) financeCharges(ctx *gin.Context) {
	a.financeJSONPage(ctx, `SELECT c.id::TEXT,c.assessed_at,jsonb_build_object(
		'id',c.id,'tripId',c.trip_id,'driverId',c.driver_id,'ruleId',c.rule_id,
		'ruleVersion',c.rule_snapshot->>'version','amountKobo',c.amount_kobo,'repaidKobo',COALESCE(repaid.amount_kobo,0),
		'outstandingKobo',GREATEST(c.amount_kobo-COALESCE(repaid.amount_kobo,0),0),
		'bearer',c.bearer,'fundingSource',c.funding_source,'operatingEntryId',c.operating_entry_id,
		'assessedAt',c.assessed_at)
		FROM trip_statutory_charges c
		LEFT JOIN LATERAL (SELECT SUM(amount_kobo) AS amount_kobo FROM statutory_charge_repayments WHERE charge_id=c.id) repaid ON TRUE
		WHERE c.driver_id=$1 AND ($2::TIMESTAMPTZ IS NULL OR (c.assessed_at,c.id)<($2,$3::UUID))
		ORDER BY c.assessed_at DESC,c.id DESC LIMIT $4`)
}

func (a *adminAPI) financeArrears(ctx *gin.Context) {
	a.financeJSONPage(ctx, `SELECT c.id::TEXT,c.assessed_at,jsonb_build_object(
		'id',c.id,'tripId',c.trip_id,'driverId',c.driver_id,'ruleId',c.rule_id,
		'amountKobo',c.amount_kobo,'repaidKobo',COALESCE(repaid.amount_kobo,0),
		'outstandingKobo',c.amount_kobo-COALESCE(repaid.amount_kobo,0),'assessedAt',c.assessed_at)
		FROM trip_statutory_charges c
		LEFT JOIN LATERAL (SELECT SUM(amount_kobo) AS amount_kobo FROM statutory_charge_repayments WHERE charge_id=c.id) repaid ON TRUE
		WHERE c.driver_id=$1 AND c.bearer='driver' AND c.funding_source='operating_balance'
		AND c.amount_kobo>COALESCE(repaid.amount_kobo,0)
		AND ($2::TIMESTAMPTZ IS NULL OR (c.assessed_at,c.id)<($2,$3::UUID))
		ORDER BY c.assessed_at DESC,c.id DESC LIMIT $4`)
}

func (a *adminAPI) financeSettlements(ctx *gin.Context) {
	a.financeJSONPage(ctx, `SELECT s.trip_id::TEXT,s.created_at,jsonb_build_object(
		'tripId',s.trip_id,'driverId',s.driver_id,'riderId',s.rider_id,
		'status',s.status,'expectedAmountKobo',s.expected_amount_kobo,
		'fareAmountKobo',s.fare_amount_kobo,'noShowDebtKobo',s.no_show_debt_kobo,
		'evidence','driver_assertion','createdAt',s.created_at,'updatedAt',s.updated_at,'resolvedAt',s.resolved_at,
		'noShowAllocations',COALESCE((SELECT jsonb_agg(jsonb_build_object('debtId',a.debt_id,'status',a.status,'amountKobo',a.amount_kobo,'settledAt',a.settled_at)) FROM trip_no_show_debt_settlements a WHERE a.trip_id=s.trip_id),'[]'::JSONB),
		'actions',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',sa.id,'actorId',sa.actor_id,'action',sa.action,'createdAt',sa.created_at) ORDER BY sa.created_at,sa.id) FROM trip_settlement_actions sa WHERE sa.trip_id=s.trip_id),'[]'::JSONB))
		FROM trip_settlements s WHERE s.driver_id=$1 AND ($2::TIMESTAMPTZ IS NULL OR (s.created_at,s.trip_id)<($2,$3::UUID))
		ORDER BY s.created_at DESC,s.trip_id DESC LIMIT $4`)
}

func (a *adminAPI) financeDebtPayments(ctx *gin.Context) {
	a.financeJSONPage(ctx, `SELECT p.id::TEXT,p.created_at,jsonb_build_object(
		'id',p.id,'debtId',p.debt_id,'claimId',d.claim_id,'beneficiaryDriverId',d.beneficiary_driver_id,
		'riderId',p.rider_id,'amountKobo',p.amount_kobo,'status',p.status,
		'provider',p.provider,'paymentReference',p.provider_reference,
		'providerTransactionReference',p.provider_transaction_reference,
		'operatingEntryId',p.operating_entry_id,'createdAt',p.created_at,'confirmedAt',p.confirmed_at)
		FROM rider_no_show_debt_payments p JOIN rider_no_show_debts d ON d.id=p.debt_id
		WHERE d.beneficiary_driver_id=$1 AND ($2::TIMESTAMPTZ IS NULL OR (p.created_at,p.id)<($2,$3::UUID))
		ORDER BY p.created_at DESC,p.id DESC LIMIT $4`)
}

func (a *adminAPI) financePayments(ctx *gin.Context) {
	a.financeJSONPage(ctx, `SELECT p.id::TEXT,p.created_at,jsonb_build_object(
		'id',p.id,'tripId',p.trip_id,'riderId',p.rider_id,'driverId',p.driver_id,
		'amountKobo',p.amount,'currency',p.currency,'status',p.status,
		'paymentReference',p.payment_reference,'transactionReference',p.transaction_reference,
		'evidence','legacy_checkout','createdAt',p.created_at,'updatedAt',p.updated_at)
		FROM payments p WHERE p.driver_id=$1::TEXT
		AND ($2::TIMESTAMPTZ IS NULL OR (p.created_at,p.id)<($2,$3::UUID))
		ORDER BY p.created_at DESC,p.id DESC LIMIT $4`)
}

func (a *adminAPI) financeHandoffs(ctx *gin.Context) {
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT o.id::TEXT,o.created_at,jsonb_build_object(
		'id',o.id,'caseId',c.id,'topicCode',c.topic_code,'tripId',c.trip_id,'requesterId',c.opened_by,
		'caseStatus',c.status,'outcome',o.outcome,'referredBy',o.actor_admin_id,'referredAt',o.created_at,
		'destination','/admin/support/cases/'||c.id::TEXT)
		FROM support_case_outcomes o JOIN support_cases c ON c.id=o.case_id WHERE o.outcome='referred_to_finance'
		AND ($1::TIMESTAMPTZ IS NULL OR (o.created_at,o.id)<($1,$2::UUID))
		ORDER BY o.created_at DESC,o.id DESC LIMIT $3`, at, id, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Finance handoffs are unavailable")
		return
	}
	defer rows.Close()
	items := make([]json.RawMessage, 0, limit+1)
	times := make([]time.Time, 0, limit+1)
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var rowID string
		var created time.Time
		var payload []byte
		if rows.Scan(&rowID, &created, &payload) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Finance handoffs are unavailable")
			return
		}
		items = append(items, json.RawMessage(payload))
		ids = append(ids, rowID)
		times = append(times, created)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Finance handoffs are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		value := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: times[limit-1], ID: ids[limit-1]})
		next = &value
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}
