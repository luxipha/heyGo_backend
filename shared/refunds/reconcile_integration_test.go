package refunds

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestRefundReservationAndTerminalReconciliation(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"COMPLETED", "FAILED"} {
		t.Run(status, func(t *testing.T) {
			staff, approver, driver, supportCase := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			topup, policy, refund, initialEntry := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			for _, item := range []struct {
				id   uuid.UUID
				role string
			}{{staff, "finance"}, {approver, "finance"}} {
				if _, err := pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1,$2,'unused',$3)`, item.id, "refund-test-"+item.id.String(), item.role); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles) VALUES($1,$2,$3,ARRAY['driver'])`, driver, "refund:"+driver.String(), "refund-human:"+driver.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO support_cases(id,opened_by,audience,topic_code,subject) VALUES($1,$2,'driver','payment','Refund test')`, supportCase, driver); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id,balance_kobo,refund_reserved_kobo) VALUES($1,20000,10000)`, driver); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key) VALUES($1,$2,'topup',20000,20000,$3)`, initialEntry, driver, "refund-fixture:"+initialEntry.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO driver_operating_topups(id,driver_id,amount_kobo,provider,provider_reference,provider_transaction_reference,status,confirmed_at) VALUES($1,$2,10000,'monnify',$3,$4,'succeeded',NOW())`, topup, driver, "PAY-"+topup.String(), "MNFY-"+topup.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO refund_policy_versions(id,version,reason_codes,max_request_kobo,max_daily_kobo,effective_from,state,proposed_by,approved_by,approved_at)
				VALUES($1,(SELECT COALESCE(MAX(version),0)+1 FROM refund_policy_versions),ARRAY['duplicate_topup'],10000,50000,NOW()-INTERVAL '1 day','approved',$2,$3,NOW())`, policy, staff, approver); err != nil {
				t.Fatal(err)
			}
			reference := "HEYGO-REFUND-" + refund.String()
			if _, err := pool.Exec(ctx, `INSERT INTO refund_requests(id,topup_id,driver_id,case_id,policy_id,amount_kobo,reason_code,reason,state,proposed_by,approved_by,refund_reference,approved_at)
				VALUES($1,$2,$3,$4,$5,10000,'duplicate_topup','Duplicate top-up','provider_pending',$6,$7,$8,NOW())`, refund, topup, driver, supportCase, policy, staff, approver, reference); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
				defer stop()
				tx, err := pool.Begin(cleanup)
				if err != nil {
					t.Errorf("cleanup begin: %v", err)
					return
				}
				defer tx.Rollback(cleanup)
				for _, sql := range []string{
					`ALTER TABLE refund_actions DISABLE TRIGGER refund_actions_append_only`,
					`ALTER TABLE driver_operating_entries DISABLE TRIGGER driver_operating_entries_append_only`,
				} {
					if _, err := tx.Exec(cleanup, sql); err != nil {
						t.Errorf("cleanup trigger: %v", err)
						return
					}
				}
				steps := []struct {
					sql  string
					args []any
				}{
					{`DELETE FROM refund_actions WHERE refund_id=$1`, []any{refund}},
					{`UPDATE refund_requests SET ledger_entry_id=NULL WHERE id=$1`, []any{refund}},
					{`DELETE FROM driver_operating_entries WHERE source_key=$1`, []any{"refund:" + refund.String()}},
					{`DELETE FROM refund_requests WHERE id=$1`, []any{refund}},
					{`DELETE FROM refund_policy_versions WHERE id=$1`, []any{policy}},
					{`DELETE FROM driver_operating_topups WHERE id=$1`, []any{topup}},
					{`DELETE FROM driver_operating_entries WHERE id=$1`, []any{initialEntry}},
					{`DELETE FROM driver_operating_accounts WHERE driver_id=$1`, []any{driver}},
					{`DELETE FROM support_cases WHERE id=$1`, []any{supportCase}},
					{`DELETE FROM users WHERE id=$1`, []any{driver}},
					{`DELETE FROM admin_users WHERE id IN ($1,$2)`, []any{staff, approver}},
				}
				for _, step := range steps {
					if _, err := tx.Exec(cleanup, step.sql, step.args...); err != nil {
						t.Errorf("cleanup %s: %v", step.sql, err)
						return
					}
				}
				for _, sql := range []string{
					`ALTER TABLE refund_actions ENABLE TRIGGER refund_actions_append_only`,
					`ALTER TABLE driver_operating_entries ENABLE TRIGGER driver_operating_entries_append_only`,
				} {
					if _, err := tx.Exec(cleanup, sql); err != nil {
						t.Errorf("cleanup trigger: %v", err)
						return
					}
				}
				if err := tx.Commit(cleanup); err != nil {
					t.Errorf("cleanup commit: %v", err)
				}
			})
			if _, err := pool.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=9999 WHERE driver_id=$1`, driver); err == nil {
				t.Fatal("competing debit consumed reserved refund balance")
			}
			if err := ApplyTerminal(ctx, pool, reference, "MNFY-"+topup.String(), status, 10000, "test provider evidence"); err != nil {
				t.Fatal(err)
			}
			if err := ApplyTerminal(ctx, pool, reference, "MNFY-"+topup.String(), status, 10000, "duplicate evidence"); err != nil {
				t.Fatalf("duplicate evidence: %v", err)
			}
			var balance, reserved int64
			var count int
			if err := pool.QueryRow(ctx, `SELECT balance_kobo,refund_reserved_kobo FROM driver_operating_accounts WHERE driver_id=$1`, driver).Scan(&balance, &reserved); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM driver_operating_entries WHERE source_key=$1`, "refund:"+refund.String()).Scan(&count); err != nil {
				t.Fatal(err)
			}
			wantBalance, wantCount := int64(20000), 0
			if status == "COMPLETED" {
				wantBalance, wantCount = 10000, 1
			}
			if balance != wantBalance || reserved != 0 || count != wantCount {
				t.Fatalf("balance=%d reserved=%d entries=%d", balance, reserved, count)
			}
		})
	}
}
