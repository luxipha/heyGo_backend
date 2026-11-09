package tripcollection

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/statutory"
	"math"
	"sort"
)

// Settle receives only an independently provider-verified exact amount. All
// ledger credits, debt allocations, settlement and notifications commit once.
func Settle(ctx context.Context, pool *pgxpool.Pool, ref, txn string, amount int64) error {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var trip string
	if e = tx.QueryRow(ctx, `SELECT trip_id::text FROM rider_trip_payments WHERE payment_reference=$1`, ref).Scan(&trip); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM trips WHERE id=$1::uuid FOR UPDATE`, trip); e != nil {
		return e
	}
	var settlement, method string
	if e = tx.QueryRow(ctx, `SELECT status,collection_method FROM trip_settlements WHERE trip_id=$1::uuid FOR UPDATE`, trip).Scan(&settlement, &method); e != nil {
		return e
	}
	var rider, driver, status string
	var savedTxn *string
	var expected, fare, debt int64
	if e = tx.QueryRow(ctx, `SELECT rider_id::text,driver_id::text,status,transaction_reference,amount_kobo,fare_kobo,debt_kobo FROM rider_trip_payments WHERE trip_id=$1::uuid FOR UPDATE`, trip).Scan(&rider, &driver, &status, &savedTxn, &expected, &fare, &debt); e != nil {
		return e
	}
	if amount != expected || txn == "" || savedTxn != nil && *savedTxn != txn || method != "monnify" {
		return fmt.Errorf("trip payment verification mismatch")
	}
	if status == "success" {
		return tx.Commit(ctx)
	}
	if settlement != "pending" {
		return fmt.Errorf("trip settlement is already resolved")
	}
	type allocation struct {
		id, driver string
		amount     int64
	}
	allocations := []allocation{}
	var debtTotal int64
	rows, e := tx.Query(ctx, `SELECT s.debt_id::text,s.beneficiary_driver_id::text,s.amount_kobo FROM trip_no_show_debt_settlements s JOIN rider_no_show_debts d ON d.id=s.debt_id WHERE s.trip_id=$1::uuid AND s.status='pending' AND d.status='due' ORDER BY s.debt_id FOR UPDATE OF s,d`, trip)
	if e != nil {
		return e
	}
	for rows.Next() {
		var v allocation
		if e = rows.Scan(&v.id, &v.driver, &v.amount); e != nil {
			rows.Close()
			return e
		}
		allocations = append(allocations, v)
		debtTotal += v.amount
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if debtTotal != debt {
		return fmt.Errorf("trip debt allocation changed")
	}
	credits := map[string]int64{driver: fare}
	for _, v := range allocations {
		credits[v.driver] += v.amount
	}
	drivers := []string{}
	for id := range credits {
		drivers = append(drivers, id)
	}
	sort.Strings(drivers)
	for _, id := range drivers {
		credit := credits[id]
		if credit == 0 {
			continue
		}
		if _, e = tx.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id)VALUES($1::uuid)ON CONFLICT DO NOTHING`, id); e != nil {
			return e
		}
		var balance int64
		if e = tx.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::uuid FOR UPDATE`, id).Scan(&balance); e != nil {
			return e
		}
		if balance > math.MaxInt64-credit {
			return fmt.Errorf("Operating Balance overflow")
		}
		balance += credit
		details, _ := json.Marshal(map[string]any{"tripId": trip, "paymentReference": ref, "transactionReference": txn, "source": "monnify_trip_collection", "bankPayout": false})
		if _, e = tx.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details)VALUES($1::uuid,$2::uuid,'trip_collection',$3,$4,$5,$6::jsonb)`, uuid.NewString(), id, credit, balance, "trip-collection:"+trip+":"+id, details); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=$2,updated_at=NOW() WHERE driver_id=$1::uuid`, id, balance); e != nil {
			return e
		}
		if e = statutory.RepayArrears(ctx, tx, id, "trip_collection", &balance); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `SELECT refresh_driver_operating_market($1::uuid)`, id); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE rider_no_show_debts d SET status='settled',settled_at=NOW() FROM trip_no_show_debt_settlements s WHERE s.trip_id=$1::uuid AND s.debt_id=d.id AND s.status='pending'`, trip); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE driver_no_show_claims c SET status='settled' FROM rider_no_show_debts d JOIN trip_no_show_debt_settlements s ON s.debt_id=d.id WHERE s.trip_id=$1::uuid AND d.claim_id=c.id AND s.status='pending'`, trip); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE trip_no_show_debt_settlements SET status='settled',settled_at=NOW() WHERE trip_id=$1::uuid AND status='pending'`, trip); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE rider_trip_payments SET status='success',transaction_reference=$2,verified_at=NOW(),updated_at=NOW() WHERE trip_id=$1::uuid`, trip, txn); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE trip_settlements SET status='confirmed',resolved_at=NOW(),updated_at=NOW() WHERE trip_id=$1::uuid`, trip); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO trip_settlement_actions(trip_id,actor_id,action,note,idempotency_key)VALUES($1::uuid,$2::uuid,'confirmed','Monnify verified collection',$3)ON CONFLICT DO NOTHING`, trip, rider, "monnify:"+trip); e != nil {
		return e
	}
	recipients := []string{rider, driver}
	sort.Strings(recipients)
	for _, id := range recipients {
		payload, _ := json.Marshal(map[string]any{"tripId": trip, "status": "confirmed", "expectedAmountKobo": amount, "providerVerified": true, "collectionMethod": "monnify", "currency": "NGN"})
		if _, e = messaging.AppendEventTx(ctx, tx, id, "trip-collection:"+trip, contracts.TripEventSettlementUpdated, payload); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
