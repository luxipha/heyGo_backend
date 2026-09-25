package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNoShowDebtPaymentNotFound = errors.New("no-show debt payment not found")
var ErrNoShowDebtPaymentMismatch = errors.New("no-show debt payment verification mismatch")

// PostVerifiedNoShowDebtPayment settles the rider debt and credits its original
// claimant only after the payment provider has verified the exact payment.
func PostVerifiedNoShowDebtPayment(ctx context.Context, pool *pgxpool.Pool, paymentReference, transactionReference string, amountKobo int64) error {
	if paymentReference == "" || transactionReference == "" || amountKobo <= 0 {
		return ErrNoShowDebtPaymentMismatch
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, debtID, riderID, status string
	var expected int64
	var savedTransaction *string
	err = tx.QueryRow(ctx, `SELECT id::TEXT,debt_id::TEXT,rider_id::TEXT,amount_kobo,status,provider_transaction_reference
		FROM rider_no_show_debt_payments WHERE provider='monnify' AND provider_reference=$1 FOR UPDATE`, paymentReference).
		Scan(&id, &debtID, &riderID, &expected, &status, &savedTransaction)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoShowDebtPaymentNotFound
	}
	if err != nil {
		return err
	}
	if expected != amountKobo || (savedTransaction != nil && *savedTransaction != transactionReference) ||
		(status == "succeeded" && savedTransaction == nil) || (status != "pending" && status != "succeeded") {
		return ErrNoShowDebtPaymentMismatch
	}
	if status == "succeeded" {
		return tx.Commit(ctx)
	}
	var driverID, debtStatus, claimID, tripID string
	var beneficiaryAmount int64
	if err := tx.QueryRow(ctx, `SELECT d.beneficiary_driver_id::TEXT,d.status,d.amount_kobo,c.id::TEXT,c.trip_id::TEXT
		FROM rider_no_show_debts d JOIN driver_no_show_claims c ON c.id=d.claim_id
		WHERE d.id=$1::UUID AND d.rider_id=$2::UUID FOR UPDATE OF d`, debtID, riderID).
		Scan(&driverID, &debtStatus, &beneficiaryAmount, &claimID, &tripID); err != nil {
		return err
	}
	if debtStatus != "due" || beneficiaryAmount != expected {
		return ErrNoShowDebtPaymentMismatch
	}
	if _, err := tx.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id,balance_kobo) VALUES($1::UUID,0) ON CONFLICT DO NOTHING`, driverID); err != nil {
		return err
	}
	var before int64
	if err := tx.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID FOR UPDATE`, driverID).Scan(&before); err != nil {
		return err
	}
	if before > 9223372036854775807-amountKobo {
		return fmt.Errorf("no-show claimant Operating Balance overflow")
	}
	after := before + amountKobo
	entryID := uuid.NewString()
	details := map[string]any{"debtId": debtID, "paymentId": id, "paymentReference": paymentReference, "transactionReference": transactionReference, "source": "rider_direct_payment"}
	if _, err := tx.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details)
		VALUES($1::UUID,$2::UUID,'no_show_transfer',$3,$4,$5,$6::JSONB)`, entryID, driverID, amountKobo, after,
		"no-show-debt-payment:"+transactionReference, details); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=$2,updated_at=NOW() WHERE driver_id=$1::UUID`, driverID, after); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE rider_no_show_debts SET status='settled',settled_at=NOW() WHERE id=$1::UUID AND status='due'`, debtID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE driver_no_show_claims SET status='settled' WHERE id=(SELECT claim_id FROM rider_no_show_debts WHERE id=$1::UUID) AND status='approved'`, debtID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE rider_no_show_debt_payments SET status='succeeded',provider_transaction_reference=$2,operating_entry_id=$3::UUID,confirmed_at=NOW()
		WHERE id=$1::UUID AND status='pending'`, id, transactionReference, entryID); err != nil {
		return err
	}
	data := []byte(fmt.Sprintf(`{"tripId":%q,"claimId":%q,"debtId":%q,"paymentId":%q,"status":"settled","feeKobo":%d,"amountKobo":%d,"currency":"NGN","settlementMethod":"direct_company_payment"}`, tripID, claimID, debtID, id, amountKobo, amountKobo))
	for _, recipient := range []string{driverID, riderID} {
		if _, err := messaging.AppendEventTx(ctx, tx, recipient, "no-show-debt-payment:"+transactionReference, "trip.event.no_show_claim_updated", data); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT refresh_driver_operating_market($1::UUID)`, driverID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
