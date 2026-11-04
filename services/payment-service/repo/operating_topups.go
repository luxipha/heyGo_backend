package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/operatingcredits"
	"github.com/luxipha/heyGo_backend/shared/statutory"
)

var ErrTopupNotFound = errors.New("operating top-up not found")
var ErrTopupMismatch = errors.New("operating top-up verification mismatch")

type OperatingTopupPosting struct {
	DriverID         string
	BalanceAfterKobo int64
	AlreadyPosted    bool
}

// PostVerifiedOperatingTopup accepts only values already checked against a
// server-side Monnify verification result. The checkout amount and both
// provider references are checked again under a row lock before posting.
func PostVerifiedOperatingTopup(ctx context.Context, pool *pgxpool.Pool, paymentReference, transactionReference string, amountKobo int64) (*OperatingTopupPosting, error) {
	if paymentReference == "" || transactionReference == "" || amountKobo <= 0 {
		return nil, ErrTopupMismatch
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var id, driverID, status string
	var expected int64
	var savedTransactionReference *string
	err = tx.QueryRow(ctx, `SELECT id::TEXT,driver_id::TEXT,amount_kobo,status,provider_transaction_reference
		FROM driver_operating_topups WHERE provider='monnify' AND provider_reference=$1 FOR UPDATE`, paymentReference).
		Scan(&id, &driverID, &expected, &status, &savedTransactionReference)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTopupNotFound
	}
	if err != nil {
		return nil, err
	}
	if expected != amountKobo || (savedTransactionReference != nil && *savedTransactionReference != transactionReference) ||
		(status == "succeeded" && savedTransactionReference == nil) {
		return nil, ErrTopupMismatch
	}
	if status != "pending" && status != "succeeded" {
		return nil, ErrTopupMismatch
	}
	if _, err := tx.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id) VALUES($1::UUID) ON CONFLICT DO NOTHING`, driverID); err != nil {
		return nil, err
	}
	posting := &OperatingTopupPosting{DriverID: driverID, AlreadyPosted: status == "succeeded"}
	if status == "succeeded" {
		if err := tx.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID`, driverID).Scan(&posting.BalanceAfterKobo); err != nil {
			return nil, err
		}
		return posting, tx.Commit(ctx)
	}
	// Account locking serializes concurrent top-ups and post-trip debits.
	var before int64
	if err := tx.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID FOR UPDATE`, driverID).Scan(&before); err != nil {
		return nil, err
	}
	if amountKobo > 0 && before > 9223372036854775807-amountKobo {
		return nil, fmt.Errorf("operating balance overflow")
	}
	posting.BalanceAfterKobo = before + amountKobo
	if _, err := tx.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details)
		VALUES($1::UUID,$2::UUID,'topup',$3,$4,$5,jsonb_build_object('provider','monnify','topupId',$6::TEXT,'paymentReference',$7::TEXT,'transactionReference',$8::TEXT))`,
		uuid.NewString(), driverID, amountKobo, posting.BalanceAfterKobo, "monnify:"+transactionReference, id, paymentReference, transactionReference); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=$2,updated_at=NOW() WHERE driver_id=$1::UUID`, driverID, posting.BalanceAfterKobo); err != nil {
		return nil, err
	}
	// Confirmed credits repay previously assessed statutory arrears before
	// remaining available as spendable Operating Balance.
	if err := repayStatutoryArrears(ctx, tx, driverID, transactionReference, &posting.BalanceAfterKobo); err != nil {
		return nil, err
	}
	if err := settleCollectedNoShowDebts(ctx, tx, driverID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE driver_operating_topups SET status='succeeded',confirmed_at=NOW(),provider_transaction_reference=$2
		WHERE id=$1::UUID`, id, transactionReference); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT refresh_driver_operating_market($1::UUID)`, driverID); err != nil {
		return nil, err
	}
	notificationData, err := json.Marshal(map[string]any{"kind": "topup", "deltaKobo": amountKobo, "balanceAfterKobo": posting.BalanceAfterKobo})
	if err != nil {
		return nil, err
	}
	if _, err := messaging.AppendEventTx(ctx, tx, driverID, "operating-topup:"+transactionReference, "driver.operating_balance.updated", notificationData); err != nil {
		return nil, err
	}
	return posting, tx.Commit(ctx)
}

// settleCollectedNoShowDebts retries rider-paid transfers that were deferred
// because the collecting driver's Operating Balance could not fund them.
func settleCollectedNoShowDebts(ctx context.Context, tx pgx.Tx, collectorID string) error {
	return operatingcredits.SettleCollectedDebts(ctx, tx, collectorID, "topup")
}

func repayStatutoryArrears(ctx context.Context, tx pgx.Tx, driverID, transactionReference string, balance *int64) error {
	return statutory.RepayArrears(ctx, tx, driverID, "topup", balance)
}
