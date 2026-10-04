package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type staffOperatingEntry struct {
	ID               string    `json:"id"`
	DriverID         string    `json:"driverId"`
	Kind             string    `json:"kind"`
	DeltaKobo        int64     `json:"deltaKobo"`
	BalanceAfterKobo int64     `json:"balanceAfterKobo"`
	CreatedAt        time.Time `json:"createdAt"`
	SourceType       *string   `json:"sourceType,omitempty"`
	SourceID         *string   `json:"sourceId,omitempty"`
}

type staffFinanceCase struct {
	ID          string    `json:"id"`
	TopicCode   string    `json:"topicCode"`
	TripID      *string   `json:"tripId"`
	RequesterID string    `json:"requesterId"`
	Status      string    `json:"status"`
	Priority    string    `json:"priority"`
	CreatedAt   time.Time `json:"createdAt"`
}

type staffSettlementDispute struct {
	TripID             string    `json:"tripId"`
	DriverID           string    `json:"driverId"`
	RiderID            string    `json:"riderId"`
	ExpectedAmountKobo int64     `json:"expectedAmountKobo"`
	Status             string    `json:"status"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

func registerAdminFinanceReadRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/finance/operating-accounts/:driverID", a.staffOperatingAccount)
	group.GET("/finance/operating-entries", a.staffOperatingEntries)
	group.GET("/finance/dispute-cases", a.staffFinanceDisputes)
	group.GET("/finance/settlement-disputes", a.staffSettlementDisputes)
	registerAdminFinanceContextRoutes(group, a)
}

func (a *adminAPI) staffSettlementDisputes(ctx *gin.Context) {
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT trip_id::TEXT,driver_id::TEXT,rider_id::TEXT,expected_amount_kobo,status,updated_at FROM trip_settlements WHERE status='disputed' AND ($1::TIMESTAMPTZ IS NULL OR (updated_at,trip_id)<($1,$2::UUID)) ORDER BY updated_at DESC,trip_id DESC LIMIT $3`, at, id, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Settlement disputes are unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffSettlementDispute, 0, limit+1)
	for rows.Next() {
		var item staffSettlementDispute
		if rows.Scan(&item.TripID, &item.DriverID, &item.RiderID, &item.ExpectedAmountKobo, &item.Status, &item.UpdatedAt) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Settlement disputes are unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Settlement disputes are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: items[len(items)-1].UpdatedAt, ID: items[len(items)-1].TripID})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) staffOperatingAccount(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("driverID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_driver_id", "Valid Driver ID is required")
		return
	}
	var balance int64
	var updated time.Time
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT balance_kobo,updated_at FROM driver_operating_accounts WHERE driver_id=$1`, id).Scan(&balance, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "account_not_found", "Operating account was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Operating account is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"driverId": id.String(), "balanceKobo": balance, "updatedAt": updated}})
}

func (a *adminAPI) staffOperatingEntries(ctx *gin.Context) {
	raw := ctx.Query("driverId")
	id, err := uuid.Parse(raw)
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_driver_id", "Valid driverId filter is required")
		return
	}
	limit, at, cursorID, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT e.id::TEXT,e.driver_id::TEXT,e.kind,e.delta_kobo,e.balance_after_kobo,e.created_at,
		CASE WHEN topup.id IS NOT NULL THEN 'topup' WHEN charge.id IS NOT NULL THEN 'statutory_charge'
		WHEN repayment.id IS NOT NULL THEN 'statutory_repayment' WHEN transfer.id IS NOT NULL THEN 'no_show_transfer'
		WHEN payment.id IS NOT NULL THEN 'no_show_debt_payment' ELSE NULL END,
		COALESCE(topup.id::TEXT,charge.id::TEXT,repayment.id::TEXT,transfer.id::TEXT,payment.id::TEXT)
		FROM driver_operating_entries e
		LEFT JOIN driver_operating_topups topup ON topup.id::TEXT=e.details->>'topupId'
		LEFT JOIN trip_statutory_charges charge ON charge.operating_entry_id=e.id
		LEFT JOIN statutory_charge_repayments repayment ON repayment.operating_entry_id=e.id
		LEFT JOIN operating_balance_transfers transfer ON transfer.from_entry_id=e.id OR transfer.to_entry_id=e.id
		LEFT JOIN rider_no_show_debt_payments payment ON payment.operating_entry_id=e.id
		WHERE e.driver_id=$1 AND ($2::TIMESTAMPTZ IS NULL OR (e.created_at,e.id)<($2,$3::UUID)) ORDER BY e.created_at DESC,e.id DESC LIMIT $4`, id, at, cursorID, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Operating entries are unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffOperatingEntry, 0, limit+1)
	for rows.Next() {
		var item staffOperatingEntry
		if rows.Scan(&item.ID, &item.DriverID, &item.Kind, &item.DeltaKobo, &item.BalanceAfterKobo, &item.CreatedAt, &item.SourceType, &item.SourceID) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Operating entries are unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Operating entries are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: items[len(items)-1].CreatedAt, ID: items[len(items)-1].ID})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) staffFinanceDisputes(ctx *gin.Context) {
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT c.id::TEXT,c.topic_code,c.trip_id::TEXT,c.opened_by::TEXT,c.status,c.priority,c.created_at FROM support_cases c WHERE c.topic_code IN ('fare','payment') AND ($1::TIMESTAMPTZ IS NULL OR (c.created_at,c.id)<($1,$2::UUID)) ORDER BY c.created_at DESC,c.id DESC LIMIT $3`, at, id, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Dispute cases are unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffFinanceCase, 0, limit+1)
	for rows.Next() {
		var item staffFinanceCase
		if rows.Scan(&item.ID, &item.TopicCode, &item.TripID, &item.RequesterID, &item.Status, &item.Priority, &item.CreatedAt) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Dispute cases are unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "finance_unavailable", "Dispute cases are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: items[len(items)-1].CreatedAt, ID: items[len(items)-1].ID})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}
