package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/observe/correlation"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type tripSettlementData struct {
	TripID             string     `json:"tripId"`
	Status             string     `json:"status"`
	ExpectedAmountKobo int64      `json:"expectedAmountKobo"`
	FareAmountKobo     int64      `json:"fareAmountKobo"`
	NoShowDebtKobo     int64      `json:"noShowDebtKobo"`
	Currency           string     `json:"currency"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	ResolvedAt         *time.Time `json:"resolvedAt"`
	DriverNote         *string    `json:"driverNote,omitempty"`
}

func (a *driverAPI) tripSettlement(ctx *gin.Context) {
	tripID := ctx.Param("tripID")
	if _, err := uuid.Parse(tripID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	var result tripSettlementData
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT s.trip_id::TEXT,s.status,s.expected_amount_kobo,s.fare_amount_kobo,s.no_show_debt_kobo,s.created_at,s.updated_at,s.resolved_at,
		(SELECT note FROM trip_settlement_actions WHERE trip_id=s.trip_id AND action='disputed' ORDER BY id DESC LIMIT 1)
		FROM trip_settlements s WHERE s.trip_id=$1::UUID AND s.driver_id=$2::UUID`, tripID, driverID(ctx)).Scan(
		&result.TripID, &result.Status, &result.ExpectedAmountKobo, &result.FareAmountKobo, &result.NoShowDebtKobo, &result.CreatedAt, &result.UpdatedAt, &result.ResolvedAt, &result.DriverNote,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "settlement_not_found", "Settlement is not available for this trip")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "settlement_unavailable", "Settlement is unavailable")
		return
	}
	result.Currency = "NGN"
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: result})
}

func (a *driverAPI) confirmTripSettlement(ctx *gin.Context) {
	a.mutateTripSettlement(ctx, "confirmed", "")
}

func (a *driverAPI) disputeTripSettlement(ctx *gin.Context) {
	var body struct {
		Note string `json:"note"`
	}
	if ctx.Request.ContentLength != 0 {
		if err := ctx.ShouldBindJSON(&body); err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_dispute", "Dispute body must be valid JSON")
			return
		}
	}
	body.Note = strings.TrimSpace(body.Note)
	if len(body.Note) > 1000 {
		driverError(ctx, http.StatusBadRequest, "dispute_note_too_long", "Dispute note must be 1000 characters or fewer")
		return
	}
	a.mutateTripSettlement(ctx, "disputed", body.Note)
}

func (a *driverAPI) mutateTripSettlement(ctx *gin.Context, action, note string) {
	tripID := ctx.Param("tripID")
	if _, err := uuid.Parse(tripID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	driverID := driverID(ctx)
	dbctx := ctx.Request.Context()
	tx, err := a.pool.Begin(dbctx)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "settlement_unavailable", "Settlement is unavailable")
		return
	}
	defer tx.Rollback(dbctx)
	var riderID, status string
	var amount, noShowDebtKobo int64
	err = tx.QueryRow(dbctx, `SELECT rider_id::TEXT,status,expected_amount_kobo,no_show_debt_kobo FROM trip_settlements
		WHERE trip_id=$1::UUID AND driver_id=$2::UUID FOR UPDATE`, tripID, driverID).Scan(&riderID, &status, &amount, &noShowDebtKobo)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "settlement_not_found", "Settlement is not available for this trip")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "settlement_unavailable", "Settlement is unavailable")
		return
	}
	if status == action {
		if err := tx.Commit(dbctx); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "settlement_unavailable", "Settlement is unavailable")
			return
		}
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"tripId": tripID, "status": status, "expectedAmountKobo": amount, "currency": "NGN"}})
		return
	}
	if status != "pending" {
		driverError(ctx, http.StatusConflict, "settlement_already_resolved", "Settlement has already been confirmed or disputed")
		return
	}
	if action == "confirmed" {
		if err := settleNoShowDebtTransfers(ctx, tx, tripID, driverID); err != nil {
			if strings.Contains(err.Error(), "Operating Balance cannot fund") {
				driverError(ctx, http.StatusConflict, "no_show_transfer_insufficient_balance", "The collecting driver's Operating Balance cannot fund the approved no-show fee transfer")
			} else {
				driverError(ctx, http.StatusServiceUnavailable, "settlement_unavailable", "Settlement could not be confirmed")
			}
			return
		}
	}
	if action == "disputed" && noShowDebtKobo > 0 {
		if _, err := tx.Exec(dbctx, `UPDATE trip_no_show_debt_settlements SET status='released' WHERE trip_id=$1::UUID AND status='pending'`, tripID); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "settlement_unavailable", "Settlement could not be disputed")
			return
		}
	}
	result, err := tx.Exec(dbctx, `UPDATE trip_settlements SET status=$2,updated_at=NOW(),resolved_at=NOW()
		WHERE trip_id=$1::UUID AND status='pending'`, tripID, action)
	if err != nil || result.RowsAffected() != 1 {
		driverError(ctx, http.StatusServiceUnavailable, "settlement_unavailable", "Settlement is unavailable")
		return
	}
	key := "trip:" + tripID + ":settlement:" + action
	if _, err := tx.Exec(dbctx, `INSERT INTO trip_settlement_actions(trip_id,actor_id,action,note,idempotency_key)
		VALUES($1::UUID,$2::UUID,$3,$4,$5)`, tripID, driverID, action, nullableNote(note), key); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "settlement_unavailable", "Settlement is unavailable")
		return
	}
	attempt := 1
	if action == "disputed" {
		attempt = 2
	}
	payload, _ := json.Marshal(map[string]any{"tripId": tripID, "status": action, "expectedAmountKobo": amount, "fareAmountKobo": amount - noShowDebtKobo, "noShowDebtKobo": noShowDebtKobo, "currency": "NGN"})
	for _, recipient := range []string{riderID, driverID} {
		if _, err := tx.Exec(dbctx, `INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,correlation_id,attempt)
			VALUES($1::UUID,$2,$3::UUID,$4::JSONB,$5,$6) ON CONFLICT DO NOTHING`,
			tripID, contracts.TripEventSettlementUpdated, recipient, payload, correlation.FromContext(dbctx), attempt); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "settlement_unavailable", "Settlement is unavailable")
			return
		}
	}
	if err := tx.Commit(dbctx); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "settlement_unavailable", "Settlement is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"tripId": tripID, "status": action, "expectedAmountKobo": amount, "currency": "NGN"}})
}

func nullableNote(note string) any {
	if note == "" {
		return nil
	}
	return note
}
