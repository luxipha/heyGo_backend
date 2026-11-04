package statutory

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RepayArrears applies available Operating Balance to previously assessed charges.
// The caller must hold the account row lock and commit credits and repayments together.
func RepayArrears(ctx context.Context, tx pgx.Tx, driverID, sourceKind string, balance *int64) error {
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
			VALUES($1::UUID,$2::UUID,'statutory_charge',$3,$4,$5,jsonb_build_object('chargeId',$6::TEXT,'topupRepayment',$7::boolean,'creditSource',$8::text))`, entryID, driverID, -pay, after, "statutory-repayment:"+repaymentID, d.id, sourceKind == "topup", sourceKind); err != nil {
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
