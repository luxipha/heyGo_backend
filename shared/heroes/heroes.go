package heroes

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
)

var ErrClaim = errors.New("Referral code cannot be linked to this Driver account")

// Claim never trusts an unauthenticated phone or Hero-supplied Driver identity.
func Claim(ctx context.Context, pool *pgxpool.Pool, driverID, code string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var heroID string
	err = tx.QueryRow(ctx, `SELECT h.id::text FROM heroes_accounts h JOIN users u ON u.id=$2::uuid WHERE h.referral_code=$1 AND h.active AND 'driver'=ANY(u.roles) AND u.phone_verified_at IS NOT NULL AND COALESCE(u.phone_e164,'')<>'' AND lower(COALESCE(u.email,''))<>h.email AND NOT EXISTS(SELECT 1 FROM simulation_participants s WHERE s.actor_user_id=u.id)`, strings.ToUpper(strings.TrimSpace(code)), driverID).Scan(&heroID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrClaim
	}
	if err != nil {
		return err
	}
	// Serialize duplicate/cross-Hero claims without overwriting existing attribution.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "heroes-driver:"+driverID); err != nil {
		return err
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT hero_id::text FROM heroes_referrals WHERE driver_id=$1::uuid`, driverID).Scan(&existing)
	if err == nil {
		if existing != heroID {
			return ErrClaim
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO heroes_referrals(id,hero_id,driver_id,policy_id)SELECT $1::uuid,$2::uuid,$3::uuid,id FROM heroes_policies ORDER BY version DESC LIMIT 1`, uuid.NewString(), heroID, driverID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ProcessRewards runs through the existing authenticated maintenance job. Each
// referral uses the immutable policy captured at claim time. Monetary values
// remain integer kobo; the unique referral key protects retries and concurrency.
func ProcessRewards(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE heroes_referrals SET onboarded_at=now() WHERE id IN(SELECT r.id FROM heroes_referrals r JOIN driver_eligibility e ON e.driver_id=r.driver_id WHERE r.onboarded_at IS NULL AND e.eligible_to_drive ORDER BY r.claimed_at LIMIT 100 FOR UPDATE OF r SKIP LOCKED)`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.hero_id::text,p.reward_kobo FROM heroes_referrals r JOIN heroes_policies p ON p.id=r.policy_id JOIN users u ON u.id=r.driver_id JOIN driver_eligibility e ON e.driver_id=u.id WHERE p.enabled AND p.reward_kobo>0 AND r.qualified_at IS NULL AND u.active AND e.eligible_to_drive AND NOT EXISTS(SELECT 1 FROM simulation_participants s WHERE s.actor_user_id=u.id) AND (SELECT count(*) FROM trips t WHERE t.assigned_driver_id=u.id AND t.status='completed' AND t.created_at>=r.claimed_at AND t.rider_id<>u.id AND NOT EXISTS(SELECT 1 FROM simulation_participants s WHERE s.actor_user_id=t.rider_id))>=p.trip_target ORDER BY r.claimed_at LIMIT 100 FOR UPDATE OF r SKIP LOCKED`)
	if err != nil {
		return err
	}
	type credit struct {
		id, hero string
		amount   int64
	}
	var credits []credit
	for rows.Next() {
		var c credit
		if err = rows.Scan(&c.id, &c.hero, &c.amount); err != nil {
			rows.Close()
			return err
		}
		credits = append(credits, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range credits {
		if _, err = tx.Exec(ctx, `INSERT INTO heroes_wallet_entries(id,hero_id,referral_id,amount_kobo)VALUES($1::uuid,$2::uuid,$3::uuid,$4)ON CONFLICT(referral_id)DO NOTHING`, uuid.NewString(), c.hero, c.id, c.amount); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE heroes_referrals SET qualified_at=now()WHERE id=$1::uuid`, c.id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
