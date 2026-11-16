package handler

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/referrals"
)

var heroTransferAmountPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,2})?$`)

type heroTransferResponse struct {
	Data contracts.TransferResult `json:"data"`
}

// Confirm only a status query whose reference, amount, currency and destination
// match the immutable local payout. A 2xx initiation response is never settlement.
func applyHeroTransferStatus(ctx context.Context, pool *pgxpool.Pool, id string, result contracts.TransferResult, key []byte) error {
	var hero string
	if err := pool.QueryRow(ctx, `SELECT hero_id::text FROM hero_stage_payouts WHERE id=$1::uuid`, id).Scan(&hero); err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "hero-money:"+hero); err != nil {
		return err
	}
	var ref, stage, bank, code, old string
	var amount int64
	var sealed []byte
	err = tx.QueryRow(ctx, `SELECT p.reference,p.stage_id::text,p.bank_account_id::text,p.amount_kobo,b.bank_code,b.account_number_ciphertext,p.status FROM hero_stage_payouts p JOIN heroes_bank_accounts b ON b.id=p.bank_account_id WHERE p.id=$1::uuid FOR UPDATE OF p`, id).Scan(&ref, &stage, &bank, &amount, &code, &sealed, &old)
	if err != nil {
		return err
	}
	number, err := openAdminValue(key, sealed, heroBankNumberPurpose)
	if err != nil {
		return err
	}
	if !heroTransferAmountPattern.MatchString(result.Amount.String()) {
		return fmt.Errorf("invalid transfer amount")
	}
	got, err := refundAmountKobo(result.Amount)
	if err != nil || result.Reference != ref || got != amount || result.Currency != "NGN" || result.DestinationBankCode != code || result.DestinationAccountNumber != number {
		return fmt.Errorf("transfer status does not match payout")
	}
	switch result.Status {
	case "SUCCESS", "COMPLETED":
		if old == "REVERSED" || old == "FAILED" || old == "EXPIRED" || old == "CANCELLED" {
			return fmt.Errorf("terminal transfer status conflict")
		}
		if result.TransactionReference == "" {
			return fmt.Errorf("missing transaction reference")
		}
		_, err = tx.Exec(ctx, `INSERT INTO heroes_manual_payments(id,hero_id,bank_account_id,amount_kobo,reference,paid_at,method)VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,now(),'monnify') ON CONFLICT(id)DO NOTHING`, id, hero, bank, amount, ref)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO heroes_payment_references(reference,payment_id)VALUES($1,$2::uuid)ON CONFLICT DO NOTHING`, ref, id)
		}
		if err != nil {
			return err
		}
		var allocated bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hero_stage_payment_allocations WHERE payment_id=$1::uuid AND stage_id=$2::uuid)`, id, stage).Scan(&allocated); err != nil {
			return err
		}
		if !allocated {
			if err = referrals.AllocateStagePayment(ctx, tx, hero, id, amount, []string{stage}); err != nil {
				return err
			}
		}
	case "REVERSED":
		_, err = tx.Exec(ctx, `INSERT INTO heroes_payment_reversals(payment_id,reason)SELECT id,'Monnify confirmed transfer reversal' FROM heroes_manual_payments WHERE id=$1::uuid ON CONFLICT DO NOTHING`, id)
		if err != nil {
			return err
		}
	case "FAILED", "EXPIRED":
		if old == "SUCCESS" || old == "COMPLETED" {
			return fmt.Errorf("paid transfer cannot become failed without confirmed reversal")
		}
	case "PENDING", "AWAITING_PROCESSING", "IN_PROGRESS", "PENDING_AUTHORIZATION", "OTP_EMAIL_DISPATCH_FAILED":
		if old == "SUCCESS" || old == "COMPLETED" || old == "REVERSED" || old == "FAILED" || old == "EXPIRED" || old == "CANCELLED" {
			return fmt.Errorf("terminal transfer cannot become pending")
		}
	default:
		return fmt.Errorf("unknown transfer status")
	}
	_, err = tx.Exec(ctx, `UPDATE hero_stage_payouts SET status=$2,transaction_reference=$3,updated_at=now() WHERE id=$1::uuid`, id, result.Status, result.TransactionReference)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func RunHeroStagePayoutJobsOnce(ctx context.Context, pool *pgxpool.Pool) error {
	key, _ := hex.DecodeString(env.GetString("HEYGO_ADMIN_SETTINGS_KEY", ""))
	if len(key) != 32 {
		return nil
	}
	// Only explicitly configured Monnify stages are queued automatically. Failed
	// attempts require a Finance decision; they are never retried with a fresh ref.
	rows, err := pool.Query(ctx, `SELECT s.id::text FROM hero_referral_stages s JOIN referral_attributions a ON a.id=s.referral_id JOIN heroes_accounts h ON h.id=a.hero_id JOIN heroes_bank_accounts b ON b.id=h.payout_account_id WHERE s.credited_at IS NOT NULL AND s.payout_mode='monnify' AND b.verified_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM hero_stage_payouts p WHERE p.stage_id=s.id) AND NOT EXISTS(SELECT 1 FROM hero_stage_payment_allocations pa WHERE pa.stage_id=s.id AND NOT EXISTS(SELECT 1 FROM heroes_payment_reversals v WHERE v.payment_id=pa.payment_id)) ORDER BY s.credited_at,s.id LIMIT 25`)
	if err != nil {
		return err
	}
	var stages []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		stages = append(stages, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, stage := range stages {
		tx, e := pool.Begin(ctx)
		if e != nil {
			return e
		}
		_, e = referrals.QueueStagePayout(ctx, tx, stage, nil)
		if e == nil {
			e = tx.Commit(ctx)
		}
		tx.Rollback(ctx)
		if e != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	// Claim at most one new transfer per run; submit once, then only reconcile.
	var id string
	var ready struct {
		Data struct {
			Ready bool `json:"ready"`
		} `json:"data"`
	}
	readyErr := paymentInternalRequest(ctx, "/internal/hero-transfers", map[string]string{"action": "ready"}, &ready)
	if readyErr == nil && ready.Data.Ready {
		err = pool.QueryRow(ctx, `UPDATE hero_stage_payouts SET status='submitting',updated_at=now() WHERE id=(SELECT id FROM hero_stage_payouts WHERE status='queued' ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id::text`).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			var sealed []byte
			var ref string
			e := pool.QueryRow(ctx, `SELECT b.account_number_ciphertext,p.reference FROM hero_stage_payouts p JOIN heroes_bank_accounts b ON b.id=p.bank_account_id WHERE p.id=$1::uuid`, id).Scan(&sealed, &ref)
			var account string
			if e == nil {
				account, e = openAdminValue(key, sealed, heroBankNumberPurpose)
			}
			var response heroTransferResponse
			if e == nil {
				e = paymentInternalRequest(ctx, "/internal/hero-transfers", map[string]string{"id": id, "action": "initiate", "accountNumber": account}, &response)
			}
			// Always reconcile, including timeouts. Reusing a new reference here could pay twice.
			_, _ = pool.Exec(ctx, `UPDATE hero_stage_payouts SET status='uncertain',updated_at=now()-interval '2 minutes' WHERE id=$1::uuid AND status='submitting'`, id)
		}
	}
	rows, err = pool.Query(ctx, `SELECT id::text FROM hero_stage_payouts WHERE status NOT IN ('queued','FAILED','EXPIRED','REVERSED','CANCELLED') AND updated_at<now()-interval '1 minute' ORDER BY updated_at,id LIMIT 25`)
	if err != nil {
		return err
	}
	var pending []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range pending {
		var response heroTransferResponse
		e := paymentInternalRequest(ctx, "/internal/hero-transfers", map[string]string{"id": id, "action": "status"}, &response)
		if e == nil {
			e = applyHeroTransferStatus(ctx, pool, id, response.Data, key)
		}
		if e != nil {
			// Rotate unresolved requests without discarding their reservation.
			_, _ = pool.Exec(ctx, `UPDATE hero_stage_payouts SET updated_at=now() WHERE id=$1::uuid`, id)
			logs.L().Warnw("Hero transfer awaits reconciliation", "payoutId", id)
		}
	}
	return nil
}

func StartHeroStagePayoutJobs(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			jobCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
			if err := referrals.ProcessRewards(jobCtx, pool); err != nil && ctx.Err() == nil {
				logs.L().Warnw("Hero stage qualification unavailable", "error", err)
			}
			if err := RunHeroStagePayoutJobsOnce(jobCtx, pool); err != nil && ctx.Err() == nil {
				logs.L().Warnw("Hero payout maintenance unavailable", "error", err)
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
