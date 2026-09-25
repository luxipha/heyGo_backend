package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	driverTripHistoryDefaultLimit = 25
	driverTripHistoryMaxLimit     = 100
)

type driverTripHistoryCursor struct {
	OccurredAt time.Time
	ID         string
}

func encodeDriverTripHistoryCursor(cursor driverTripHistoryCursor) string {
	value := cursor.OccurredAt.UTC().Format(time.RFC3339Nano) + "\n" + cursor.ID
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeDriverTripHistoryCursor(value string) (driverTripHistoryCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return driverTripHistoryCursor{}, err
	}
	parts := strings.Split(string(decoded), "\n")
	if len(parts) != 2 {
		return driverTripHistoryCursor{}, errors.New("invalid cursor shape")
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return driverTripHistoryCursor{}, err
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return driverTripHistoryCursor{}, err
	}
	return driverTripHistoryCursor{OccurredAt: at.UTC(), ID: id.String()}, nil
}

func parseDriverTripHistoryLimit(value string) (int, error) {
	if value == "" {
		return driverTripHistoryDefaultLimit, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > driverTripHistoryMaxLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", driverTripHistoryMaxLimit)
	}
	return limit, nil
}

func parseDriverTripHistoryTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("timestamp must use RFC-3339")
	}
	at = at.UTC()
	return &at, nil
}

type driverTripHistoryItem struct {
	ID                 string          `json:"id"`
	Status             string          `json:"status"`
	PackageSlug        string          `json:"packageSlug"`
	Route              json.RawMessage `json:"route"`
	Currency           string          `json:"currency"`
	FareKobo           int64           `json:"fareKobo"`
	NoShowDebtKobo     int64           `json:"noShowDebtKobo"`
	AmountDueKobo      int64           `json:"amountDueKobo"`
	CreatedAt          time.Time       `json:"createdAt"`
	OccurredAt         time.Time       `json:"occurredAt"`
	StartedAt          *time.Time      `json:"startedAt,omitempty"`
	CompletedAt        *time.Time      `json:"completedAt,omitempty"`
	CancelledAt        *time.Time      `json:"cancelledAt,omitempty"`
	CancellationReason *string         `json:"cancellationReason,omitempty"`
	CancelledBy        *string         `json:"cancelledBy,omitempty"`
	SettlementStatus   *string         `json:"settlementStatus,omitempty"`
	NoShowClaimStatus  *string         `json:"noShowClaimStatus,omitempty"`
	NoShowFeeKobo      *int64          `json:"noShowFeeKobo,omitempty"`
	Rating             *int            `json:"rating,omitempty"`
	FeedbackTags       []string        `json:"feedbackTags"`
	ReceiptReference   *string         `json:"receiptReference,omitempty"`
}

func readDriverTripHistory(ctx *gin.Context, pool *pgxpool.Pool, driver string, limit int, cursor *driverTripHistoryCursor, from, to *time.Time, status string) ([]driverTripHistoryItem, error) {
	var cursorTime any
	var cursorID any
	if cursor != nil {
		cursorTime, cursorID = cursor.OccurredAt, cursor.ID
	}
	rows, err := pool.Query(ctx.Request.Context(), `SELECT t.id::TEXT,t.status,f.package_slug,f.route,
		ROUND(f.total_fare_minor)::BIGINT,t.no_show_debt_kobo,ROUND(f.total_fare_minor)::BIGINT+t.no_show_debt_kobo,
		t.created_at,COALESCE(t.completed_at,t.cancelled_at,t.updated_at,t.created_at),t.started_at,t.completed_at,t.cancelled_at,
		t.cancellation_reason,CASE WHEN t.cancellation_actor_id=t.assigned_driver_id THEN 'driver' WHEN t.cancellation_actor_id=t.rider_id THEN 'rider' ELSE NULL END,
		s.status,c.status,COALESCE(d.amount_kobo,c.fee_kobo),r.rating,COALESCE(r.feedback_tags,ARRAY[]::TEXT[]),
		CASE WHEN t.status='completed' THEN 'trip:'||t.id::TEXT ELSE NULL END
		FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id
		LEFT JOIN trip_settlements s ON s.trip_id=t.id
		LEFT JOIN driver_no_show_claims c ON c.trip_id=t.id AND c.driver_id=$1::UUID
		LEFT JOIN rider_no_show_debts d ON d.claim_id=c.id
		LEFT JOIN trip_ratings r ON r.trip_id=t.id AND r.actor_id=$1::UUID
		WHERE t.assigned_driver_id=$1::UUID AND t.status IN ('completed','cancelled')
		AND ($2::TEXT='all' OR ($2='no_show_claim' AND c.id IS NOT NULL) OR ($2 IN ('completed','cancelled') AND t.status=$2 AND NOT ($2='cancelled' AND c.id IS NOT NULL)))
		AND ($3::TIMESTAMPTZ IS NULL OR COALESCE(t.completed_at,t.cancelled_at,t.updated_at,t.created_at)>=$3)
		AND ($4::TIMESTAMPTZ IS NULL OR COALESCE(t.completed_at,t.cancelled_at,t.updated_at,t.created_at)<$4)
		AND ($5::TIMESTAMPTZ IS NULL OR (COALESCE(t.completed_at,t.cancelled_at,t.updated_at,t.created_at),t.id)<($5,$6::UUID))
		ORDER BY COALESCE(t.completed_at,t.cancelled_at,t.updated_at,t.created_at) DESC,t.id DESC LIMIT $7`,
		driver, status, from, to, cursorTime, cursorID, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]driverTripHistoryItem, 0, limit+1)
	for rows.Next() {
		var item driverTripHistoryItem
		var fare, debt int64
		if err := rows.Scan(&item.ID, &item.Status, &item.PackageSlug, &item.Route, &fare, &debt, &item.AmountDueKobo, &item.CreatedAt, &item.OccurredAt, &item.StartedAt, &item.CompletedAt, &item.CancelledAt, &item.CancellationReason, &item.CancelledBy, &item.SettlementStatus, &item.NoShowClaimStatus, &item.NoShowFeeKobo, &item.Rating, &item.FeedbackTags, &item.ReceiptReference); err != nil {
			return nil, err
		}
		item.Currency = "NGN"
		item.FareKobo = fare
		item.NoShowDebtKobo = debt
		if item.FeedbackTags == nil {
			item.FeedbackTags = []string{}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (a *driverAPI) tripHistory(ctx *gin.Context) {
	limit, err := parseDriverTripHistoryLimit(ctx.Query("limit"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	status := ctx.DefaultQuery("status", "all")
	if status != "all" && status != "completed" && status != "cancelled" && status != "no_show_claim" {
		driverError(ctx, http.StatusBadRequest, "invalid_status", "Status must be all, completed, cancelled, or no_show_claim")
		return
	}
	from, err := parseDriverTripHistoryTime(ctx.Query("from"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_from", "from must be an RFC-3339 timestamp")
		return
	}
	to, err := parseDriverTripHistoryTime(ctx.Query("to"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_to", "to must be an RFC-3339 timestamp")
		return
	}
	if from != nil && to != nil && !from.Before(*to) {
		driverError(ctx, http.StatusBadRequest, "invalid_range", "from must be earlier than to")
		return
	}
	var cursor *driverTripHistoryCursor
	if value := ctx.Query("cursor"); value != "" {
		decoded, err := decodeDriverTripHistoryCursor(value)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Trip history cursor is invalid")
			return
		}
		cursor = &decoded
	}
	items, err := readDriverTripHistory(ctx, a.pool, driverID(ctx), limit, cursor, from, to, status)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "trip_history_unavailable", "Trip history is unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		value := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: items[len(items)-1].OccurredAt, ID: items[len(items)-1].ID})
		next = &value
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

type driverTripReceipt struct {
	ReceiptReference   string          `json:"receiptReference"`
	TripID             string          `json:"tripId"`
	Status             string          `json:"status"`
	Currency           string          `json:"currency"`
	FareKobo           int64           `json:"fareKobo"`
	NoShowDebtKobo     int64           `json:"noShowDebtKobo"`
	AmountDueKobo      int64           `json:"amountDueKobo"`
	PaymentStatus      string          `json:"paymentStatus"`
	ConfirmationSource string          `json:"confirmationSource"`
	Route              json.RawMessage `json:"route"`
	CreatedAt          time.Time       `json:"createdAt"`
	CompletedAt        time.Time       `json:"completedAt"`
	ConfirmedAt        *time.Time      `json:"confirmedAt,omitempty"`
}

func readDriverTripReceipt(ctx *gin.Context, pool *pgxpool.Pool, driver, tripID string) (driverTripReceipt, error) {
	var receipt driverTripReceipt
	var paymentStatus string
	var resolvedAt *time.Time
	var completedAt *time.Time
	err := pool.QueryRow(ctx.Request.Context(), `SELECT t.id::TEXT,t.status,ROUND(f.total_fare_minor)::BIGINT,t.no_show_debt_kobo,
		ROUND(f.total_fare_minor)::BIGINT+t.no_show_debt_kobo,COALESCE(s.status,'not_recorded'),s.resolved_at,f.route,t.created_at,t.completed_at
		FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id LEFT JOIN trip_settlements s ON s.trip_id=t.id
		WHERE t.id=$1::UUID AND t.assigned_driver_id=$2::UUID`, tripID, driver).
		Scan(&receipt.TripID, &receipt.Status, &receipt.FareKobo, &receipt.NoShowDebtKobo, &receipt.AmountDueKobo, &paymentStatus, &resolvedAt, &receipt.Route, &receipt.CreatedAt, &completedAt)
	if err != nil {
		return driverTripReceipt{}, err
	}
	if receipt.Status != "completed" {
		return driverTripReceipt{}, errors.New("receipt is not available until the trip is completed")
	}
	if completedAt == nil {
		return driverTripReceipt{}, errors.New("receipt is not available until the trip is completed")
	}
	receipt.ReceiptReference = "trip:" + receipt.TripID
	receipt.Currency = "NGN"
	receipt.PaymentStatus = paymentStatus
	receipt.ConfirmationSource = "driver_entered"
	receipt.ConfirmedAt = resolvedAt
	receipt.CompletedAt = *completedAt
	return receipt, nil
}

func (a *driverAPI) tripReceipt(ctx *gin.Context) {
	tripID := ctx.Param("tripID")
	if _, err := uuid.Parse(tripID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	receipt, err := readDriverTripReceipt(ctx, a.pool, driverID(ctx), tripID)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "trip_not_found", "Trip was not found")
		return
	}
	if err != nil {
		if strings.Contains(err.Error(), "not available until") {
			driverError(ctx, http.StatusConflict, "receipt_not_ready", err.Error())
		} else {
			driverError(ctx, http.StatusServiceUnavailable, "receipt_unavailable", "Trip receipt is unavailable")
		}
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: receipt})
}
