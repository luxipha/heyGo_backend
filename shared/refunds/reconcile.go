package refunds

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplyTerminal consumes or releases a reservation only after authenticated
// provider evidence names the exact refund, source transaction, and amount.
// Duplicate evidence is harmless; contradictory terminal evidence is rejected.
func ApplyTerminal(ctx context.Context, pool *pgxpool.Pool, reference, transactionReference, status string, amountKobo int64, source string) error {
	status = strings.ToUpper(status)
	if reference == "" || transactionReference == "" || amountKobo <= 0 || (status != "COMPLETED" && status != "FAILED") {
		return errors.New("invalid terminal refund evidence")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, driverID, state, originalReference string
	var amount int64
	err = tx.QueryRow(ctx, `SELECT r.id::TEXT,r.driver_id::TEXT,r.state,r.amount_kobo,t.provider_transaction_reference
		FROM refund_requests r JOIN driver_operating_topups t ON t.id=r.topup_id
		WHERE r.refund_reference=$1 FOR UPDATE OF r`, reference).
		Scan(&id, &driverID, &state, &amount, &originalReference)
	if err != nil {
		return err
	}
	if originalReference != transactionReference || amount != amountKobo {
		return errors.New("provider refund evidence does not match source")
	}
	terminal := "failed"
	if status == "COMPLETED" {
		terminal = "completed"
	}
	if state == terminal {
		return nil
	}
	if state == "completed" || state == "failed" || (state != "provider_pending" && state != "needs_reconciliation") {
		return fmt.Errorf("refund state %s conflicts with terminal evidence", state)
	}
	var balance, reserved int64
	err = tx.QueryRow(ctx, `SELECT balance_kobo,refund_reserved_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID FOR UPDATE`, driverID).Scan(&balance, &reserved)
	if err != nil || reserved < amount {
		return errors.New("refund reservation missing")
	}
	var entryID *uuid.UUID
	if terminal == "completed" {
		entry := uuid.New()
		after := balance - amount
		_, err = tx.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=$2,refund_reserved_kobo=refund_reserved_kobo-$3,updated_at=NOW()
			WHERE driver_id=$1::UUID`, driverID, after, amount)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details)
				VALUES($1,$2::UUID,'reversal',$3,$4,$5,jsonb_build_object('refundId',$6::TEXT,'refundReference',$7::TEXT))`,
				entry, driverID, -amount, after, "refund:"+id, id, reference)
		}
		entryID = &entry
	} else {
		_, err = tx.Exec(ctx, `UPDATE driver_operating_accounts SET refund_reserved_kobo=refund_reserved_kobo-$2,updated_at=NOW() WHERE driver_id=$1::UUID`, driverID, amount)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE refund_requests SET state=$2,provider_status=$3,provider_transaction_reference=$4,
			ledger_entry_id=$5,completed_at=CASE WHEN $2='completed' THEN NOW() ELSE NULL END,updated_at=NOW() WHERE id=$1::UUID`,
			id, terminal, status, transactionReference, entryID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO refund_actions(id,refund_id,action,reason,before_state,after_state)
			VALUES($1,$2::UUID,$3,$4,$5,$6)`, uuid.New(), id, "provider_"+terminal, source, state, terminal)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func MarkUncertain(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, reason string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state string
	err = tx.QueryRow(ctx, `SELECT state FROM refund_requests WHERE id=$1 FOR UPDATE`, id).Scan(&state)
	if err != nil {
		return err
	}
	if state != "provider_pending" {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE refund_requests SET state='needs_reconciliation',updated_at=NOW() WHERE id=$1`, id)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO refund_actions(id,refund_id,action,reason,before_state,after_state)
			VALUES($1,$2,'provider_uncertain',$3,'provider_pending','needs_reconciliation')`, uuid.New(), id, reason)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ClaimApproved records the send intention before any network call. Once
// claimed, a crash can only resume via status lookup, never a second send.
func ClaimApproved(ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer tx.Rollback(ctx)
	var id uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM refund_requests WHERE state='approved' ORDER BY approved_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE refund_requests SET state='provider_pending',updated_at=NOW() WHERE id=$1`, id)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO refund_actions(id,refund_id,action,reason,before_state,after_state)
			VALUES($1,$2,'provider_send_claimed','Durable send intention','approved','provider_pending')`, uuid.New(), id)
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}
