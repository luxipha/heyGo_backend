package handler

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	operatingTransactionsDefaultLimit = 50
	operatingTransactionsMaximumLimit = 100
)

type operatingBalanceTransaction struct {
	ID               string         `json:"id"`
	Type             string         `json:"type"`
	DeltaKobo        int64          `json:"deltaKobo"`
	BalanceAfterKobo int64          `json:"balanceAfterKobo"`
	Details          map[string]any `json:"details"`
	CreatedAt        time.Time      `json:"createdAt"`
}

type operatingTransactionCursor struct {
	CreatedAt time.Time
	ID        string
}

func encodeOperatingTransactionCursor(entry operatingBalanceTransaction) string {
	value := entry.CreatedAt.UTC().Format(time.RFC3339Nano) + "\n" + entry.ID
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeOperatingTransactionCursor(value string) (operatingTransactionCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return operatingTransactionCursor{}, fmt.Errorf("decode transaction cursor: %w", err)
	}
	parts := strings.Split(string(decoded), "\n")
	if len(parts) != 2 {
		return operatingTransactionCursor{}, errors.New("invalid transaction cursor shape")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return operatingTransactionCursor{}, fmt.Errorf("parse transaction cursor timestamp: %w", err)
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return operatingTransactionCursor{}, fmt.Errorf("parse transaction cursor ID: %w", err)
	}
	return operatingTransactionCursor{CreatedAt: createdAt.UTC(), ID: parts[1]}, nil
}

func parseOperatingTransactionsLimit(value string) (int, error) {
	if value == "" {
		return operatingTransactionsDefaultLimit, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > operatingTransactionsMaximumLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", operatingTransactionsMaximumLimit)
	}
	return limit, nil
}

func readOperatingBalanceTransactions(ctx *gin.Context, pool *pgxpool.Pool, driver string, limit int, cursor *operatingTransactionCursor) ([]operatingBalanceTransaction, error) {
	var cursorTime any
	var cursorID any
	if cursor != nil {
		cursorTime = cursor.CreatedAt
		cursorID = cursor.ID
	}
	rows, err := pool.Query(ctx.Request.Context(), `SELECT id::TEXT,kind,delta_kobo,balance_after_kobo,details,created_at
		FROM driver_operating_entries
		WHERE driver_id=$1::UUID AND ($2::TIMESTAMPTZ IS NULL OR (created_at,id)<($2::TIMESTAMPTZ,$3::UUID))
		ORDER BY created_at DESC,id DESC LIMIT $4`, driver, cursorTime, cursorID, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]operatingBalanceTransaction, 0, limit+1)
	for rows.Next() {
		var entry operatingBalanceTransaction
		if err := rows.Scan(&entry.ID, &entry.Type, &entry.DeltaKobo, &entry.BalanceAfterKobo, &entry.Details, &entry.CreatedAt); err != nil {
			return nil, err
		}
		if entry.Details == nil {
			entry.Details = map[string]any{}
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (a *driverAPI) operatingBalanceTransactions(ctx *gin.Context) {
	limit, err := parseOperatingTransactionsLimit(ctx.Query("limit"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	var cursor *operatingTransactionCursor
	if value := ctx.Query("cursor"); value != "" {
		decoded, err := decodeOperatingTransactionCursor(value)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_cursor", "Transaction cursor is invalid")
			return
		}
		cursor = &decoded
	}
	entries, err := readOperatingBalanceTransactions(ctx, a.pool, driverID(ctx), limit, cursor)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "operating_balance_transactions_unavailable", "Operating Balance transactions are unavailable")
		return
	}
	var next *string
	if len(entries) > limit {
		entries = entries[:limit]
		value := encodeOperatingTransactionCursor(entries[len(entries)-1])
		next = &value
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: entries, Meta: contracts.APIMeta{NextCursor: next}})
}
