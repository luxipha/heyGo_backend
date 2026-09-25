package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	rows, err := tx.Query(ctx, `SELECT s.trip_id::TEXT,s.debt_id::TEXT,d.claim_id::TEXT,d.rider_id::TEXT,s.beneficiary_driver_id::TEXT,s.amount_kobo
		FROM trip_no_show_debt_settlements s JOIN rider_no_show_debts d ON d.id=s.debt_id JOIN trips t ON t.id=s.trip_id
		WHERE t.assigned_driver_id=$1::UUID AND s.status='collection_pending' AND d.status='collection_pending'
		ORDER BY s.trip_id,s.debt_id FOR UPDATE OF s,d`, collectorID)
	if err != nil {
		return err
	}
	type transfer struct {
		trip, debt, claim, rider, beneficiary string
		amount                                int64
	}
	items := []transfer{}
	for rows.Next() {
		var item transfer
		if err := rows.Scan(&item.trip, &item.debt, &item.claim, &item.rider, &item.beneficiary, &item.amount); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(items) == 0 {
		return nil
	}
	var allowNegative bool
	var maxNegative int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(p.allow_negative,FALSE),COALESCE(p.maximum_negative_kobo,0)
		FROM drivers d LEFT JOIN LATERAL (SELECT allow_negative,maximum_negative_kobo FROM operating_balance_policies p
		WHERE p.market_code=d.online_market_code AND p.status='approved' AND p.effective_from<=NOW() AND (p.effective_until IS NULL OR p.effective_until>NOW()) ORDER BY p.version DESC LIMIT 1) p ON TRUE
		WHERE d.id=$1::UUID`, collectorID).Scan(&allowNegative, &maxNegative); err != nil {
		return err
	}
	minimum := int64(0)
	if allowNegative {
		minimum = -maxNegative
	}
	var collectorBalance int64
	if err := tx.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID FOR UPDATE`, collectorID).Scan(&collectorBalance); err != nil {
		return err
	}
	for _, item := range items {
		fromDelta := -item.amount
		if collectorID == item.beneficiary {
			fromDelta = 0
		}
		fromAfter := collectorBalance + fromDelta
		if fromAfter < minimum {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id,balance_kobo) VALUES($1::UUID,0) ON CONFLICT DO NOTHING`, item.beneficiary); err != nil {
			return err
		}
		var toBefore int64
		if err := tx.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID FOR UPDATE`, item.beneficiary).Scan(&toBefore); err != nil {
			return err
		}
		toAfter := toBefore + item.amount
		transferID := uuid.NewString()
		var fromEntry any
		if fromDelta != 0 {
			fromEntryID := uuid.NewString()
			details := map[string]any{"transferId": transferID, "debtId": item.debt, "tripId": item.trip, "role": "source", "retriedAfterTopup": true}
			if _, err := tx.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details) VALUES($1::UUID,$2::UUID,'no_show_transfer',$3,$4,$5,$6::JSONB)`, fromEntryID, collectorID, fromDelta, fromAfter, "no-show:"+item.debt+":source", details); err != nil {
				return err
			}
			fromEntry = fromEntryID
		}
		toEntryID := uuid.NewString()
		toDetails := map[string]any{"transferId": transferID, "debtId": item.debt, "tripId": item.trip, "role": "beneficiary", "retriedAfterTopup": true}
		if _, err := tx.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details) VALUES($1::UUID,$2::UUID,'no_show_transfer',$3,$4,$5,$6::JSONB)`, toEntryID, item.beneficiary, item.amount, toAfter, "no-show:"+item.debt+":beneficiary", toDetails); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=$2,updated_at=NOW() WHERE driver_id=$1::UUID`, collectorID, fromAfter); err != nil {
			return err
		}
		if item.beneficiary != collectorID {
			if _, err := tx.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=$2,updated_at=NOW() WHERE driver_id=$1::UUID`, item.beneficiary, toAfter); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=$2,updated_at=NOW() WHERE driver_id=$1::UUID`, collectorID, toAfter); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operating_balance_transfers(id,debt_id,trip_id,from_driver_id,to_driver_id,amount_kobo,from_entry_id,to_entry_id) VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,$5::UUID,$6,$7::UUID,$8::UUID)`, transferID, item.debt, item.trip, collectorID, item.beneficiary, item.amount, fromEntry, toEntryID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE rider_no_show_debts SET status='settled',settled_at=NOW() WHERE id=$1::UUID AND status='collection_pending'`, item.debt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE driver_no_show_claims SET status='settled' WHERE id=$1::UUID AND status='approved'`, item.claim); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE trip_no_show_debt_settlements SET status='settled',settled_at=NOW() WHERE trip_id=$1::UUID AND debt_id=$2::UUID AND status='collection_pending'`, item.trip, item.debt); err != nil {
			return err
		}
		data, err := json.Marshal(map[string]any{"tripId": item.trip, "claimId": item.claim, "status": "settled", "feeKobo": item.amount, "currency": "NGN"})
		if err != nil {
			return err
		}
		for _, recipient := range []string{item.beneficiary, collectorID, item.rider} {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::UUID::TEXT,0))`, recipient); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO user_events(recipient_id,source_event_id,type,data) VALUES($1::UUID,$2,'trip.event.no_show_claim_updated',$3::JSONB)`, recipient, uuid.NewString(), data); err != nil {
				return err
			}
		}
		collectorBalance = fromAfter
		if collectorID == item.beneficiary {
			collectorBalance = toAfter
		}
	}
	return nil
}

func repayStatutoryArrears(ctx context.Context, tx pgx.Tx, driverID, transactionReference string, balance *int64) error {
	rows, err := tx.Query(ctx, `SELECT c.id::TEXT,c.amount_kobo
		FROM trip_statutory_charges c WHERE c.driver_id=$1::UUID AND c.bearer='driver' AND c.funding_source='operating_balance'
		AND c.amount_kobo>COALESCE((SELECT SUM(r.amount_kobo) FROM statutory_charge_repayments r WHERE r.charge_id=c.id),0)
		ORDER BY c.assessed_at,c.id FOR UPDATE OF c`, driverID)
	if err != nil {
		return err
	}
	type due struct {
		id     string
		amount int64
	}
	dues := make([]due, 0)
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.amount); err != nil {
			rows.Close()
			return err
		}
		dues = append(dues, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, d := range dues {
		var paid int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount_kobo),0)::BIGINT FROM statutory_charge_repayments WHERE charge_id=$1::UUID`, d.id).Scan(&paid); err != nil {
			return err
		}
		left := d.amount - paid
		if left <= 0 || *balance <= 0 {
			continue
		}
		pay := left
		if pay > *balance {
			pay = *balance
		}
		if pay <= 0 {
			continue
		}
		after := *balance - pay
		repaymentID := uuid.NewString()
		entryID := uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details)
			VALUES($1::UUID,$2::UUID,'statutory_charge',$3,$4,$5,jsonb_build_object('chargeId',$6::TEXT,'topupRepayment',TRUE))`, entryID, driverID, -pay, after, "statutory-repayment:"+repaymentID, d.id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO statutory_charge_repayments(id,charge_id,driver_id,amount_kobo,operating_entry_id) VALUES($1::UUID,$2::UUID,$3::UUID,$4,$5::UUID)`, repaymentID, d.id, driverID, pay, entryID); err != nil {
			return err
		}
		*balance = after
	}
	_, err = tx.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=$2,updated_at=NOW() WHERE driver_id=$1::UUID`, driverID, *balance)
	return err
}
