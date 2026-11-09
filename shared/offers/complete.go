package offers

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Promotional credit is granted only to a completed, reserved trip. The
// existing SpendRideCredit path then spends it and reimburses the Driver.
// SQL/ledger compatibility and exactly-once insertion are covered in the
// isolated settlement test. Called in the trip completion transaction.
func Complete(ctx context.Context, tx pgx.Tx, trip string) error {
	var rider, driver string
	var fare, amount int64
	e := tx.QueryRow(ctx, `SELECT t.rider_id::text,t.assigned_driver_id::text,ROUND(f.total_fare_minor)::bigint,COALESCE((SELECT sum(discount_kobo) FROM rider_offer_uses WHERE trip_id=t.id AND status='reserved'),0)::bigint FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.id=$1::uuid AND t.status='completed' FOR UPDATE OF t`, trip).Scan(&rider, &driver, &fare, &amount)
	if e != nil {
		return e
	}
	amount = min(amount, fare)
	if amount > 0 && rider != driver {
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('referral-approval-batch'))`); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO rider_credit_accounts(rider_id)VALUES($1::uuid)ON CONFLICT DO NOTHING`, rider); e != nil {
			return e
		}
		var balance int64
		if e = tx.QueryRow(ctx, `SELECT balance_kobo FROM rider_credit_accounts WHERE rider_id=$1::uuid FOR UPDATE`, rider).Scan(&balance); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `INSERT INTO rider_credit_entries(id,rider_id,source_key,delta_kobo,balance_after_kobo,kind,trip_id)VALUES(gen_random_uuid(),$1::uuid,$2,$3,$4,'promotion_reward',$5::uuid)ON CONFLICT(source_key)DO NOTHING`, rider, "offer-trip:"+trip, amount, balance+amount, trip)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 1 {
			if _, e = tx.Exec(ctx, `UPDATE rider_credit_accounts SET balance_kobo=balance_kobo+$2,updated_at=NOW() WHERE rider_id=$1::uuid`, rider, amount); e != nil {
				return e
			}
		}
	}
	_, e = tx.Exec(ctx, `UPDATE rider_offer_uses SET status='redeemed' WHERE trip_id=$1::uuid AND status='reserved'`, trip)
	return e
}
