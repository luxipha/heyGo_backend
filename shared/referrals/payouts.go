package referrals

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// QueueStagePayout reserves a credited stage against manual and provider payments.
// Caller commits the reservation before any external transfer request.
func QueueStagePayout(ctx context.Context, tx pgx.Tx, stage string, staff *string) (string, error) {
	var hero string
	if err := tx.QueryRow(ctx, `SELECT a.hero_id::text FROM hero_referral_stages s JOIN referral_attributions a ON a.id=s.referral_id WHERE s.id=$1::uuid`, stage).Scan(&hero); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "hero-money:"+hero); err != nil {
		return "", err
	}
	var bank *string
	var amount int64
	var eligible bool
	err := tx.QueryRow(ctx, `SELECT h.payout_account_id::text,hero_stage_amount(s.id),s.credited_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM hero_stage_payment_allocations pa WHERE pa.stage_id=s.id AND NOT EXISTS(SELECT 1 FROM heroes_payment_reversals v WHERE v.payment_id=pa.payment_id)) FROM hero_referral_stages s JOIN heroes_accounts h ON h.id=$2::uuid WHERE s.id=$1::uuid FOR UPDATE OF s`, stage, hero).Scan(&bank, &amount, &eligible)
	if err != nil {
		return "", err
	}
	if !eligible || bank == nil {
		return "", errors.New("Stage is not payable or bank details are missing")
	}
	var verified bool
	if err = tx.QueryRow(ctx, `SELECT verified_at IS NOT NULL AND bank_code<>'' FROM heroes_bank_accounts WHERE id=$1::uuid`, *bank).Scan(&verified); err != nil {
		return "", err
	}
	if !verified {
		return "", errors.New("Hero must add a verified bank account for Monnify")
	}
	var old string
	err = tx.QueryRow(ctx, `SELECT id::text FROM hero_stage_payouts WHERE stage_id=$1::uuid AND status NOT IN ('FAILED','EXPIRED','REVERSED','CANCELLED')`, stage).Scan(&old)
	if err == nil {
		return old, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var config int64
	if err = tx.QueryRow(ctx, `SELECT version FROM admin_monnify_settings WHERE enabled ORDER BY version DESC LIMIT 1`).Scan(&config); err != nil {
		return "", errors.New("Enable and configure Monnify first")
	}
	// Do not fall back to a previous enabled settings version after Monnify is paused.
	var enabled bool
	if err = tx.QueryRow(ctx, `SELECT enabled FROM admin_monnify_settings ORDER BY version DESC LIMIT 1`).Scan(&enabled); err != nil || !enabled {
		return "", errors.New("Monnify is disabled")
	}
	id := uuid.NewString()
	_, err = tx.Exec(ctx, `INSERT INTO hero_stage_payouts(id,stage_id,hero_id,bank_account_id,amount_kobo,reference,monnify_config_version,requested_by)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8::uuid)`, id, stage, hero, *bank, amount, "hgs-"+id, config, staff)
	return id, err
}
