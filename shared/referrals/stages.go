package referrals

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// StageProgress is shared by Finance and Heroes. All monetary values are kobo.
type StageProgress struct {
	PayoutMode       string     `json:"payoutMode"`
	PayoutStatus     *string    `json:"payoutStatus"`
	PayoutID         *string    `json:"payoutId"`
	ID               string     `json:"id"`
	ReferralID       string     `json:"referralId"`
	HeroID           string     `json:"heroId"`
	Hero             string     `json:"hero"`
	Driver           string     `json:"driver"`
	Position         int        `json:"position"`
	TripTarget       int        `json:"tripTarget"`
	RewardKobo       int64      `json:"rewardKobo"`
	ApprovalMode     string     `json:"approvalMode"`
	CompletedTrips   int        `json:"completedTrips"`
	QualifiedAt      *time.Time `json:"qualifiedAt"`
	CreditedAt       *time.Time `json:"creditedAt"`
	ApprovedBy       *string    `json:"approvedBy"`
	PaidAt           *time.Time `json:"paidAt"`
	PaymentReference *string    `json:"paymentReference"`
	DeadlineAt       *time.Time `json:"deadlineAt"`
}

type stageQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func ListStages(ctx context.Context, q stageQuerier, hero string, offset int) ([]StageProgress, error) {
	rows, err := q.Query(ctx, `SELECT s.id::text,a.id::text,a.hero_id::text,h.name,COALESCE(NULLIF(dp.display_name,''),'Driver'),s.position,s.trip_target,s.reward_kobo,s.approval_mode,`+ProgressSQL+`,s.qualified_at,s.credited_at,s.approved_by::text,p.paid_at,p.reference,a.deadline_at,s.payout_mode,po.status,po.id::text
 FROM hero_referral_stages s JOIN referral_attributions a ON a.id=s.referral_id JOIN heroes_accounts h ON h.id=a.hero_id LEFT JOIN driver_profiles dp ON dp.driver_id=a.invitee_id
 LEFT JOIN LATERAL(SELECT p.paid_at,p.reference FROM hero_stage_payment_allocations pa JOIN heroes_manual_payments p ON p.id=pa.payment_id WHERE pa.stage_id=s.id AND NOT EXISTS(SELECT 1 FROM heroes_payment_reversals v WHERE v.payment_id=p.id) ORDER BY p.created_at DESC LIMIT 1)p ON true
 LEFT JOIN LATERAL(SELECT status,id FROM hero_stage_payouts WHERE stage_id=s.id ORDER BY created_at DESC,id DESC LIMIT 1)po ON true
 WHERE ($1='' OR a.hero_id::text=$1) ORDER BY a.linked_at DESC,a.id,s.position LIMIT 201 OFFSET $2`, hero, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []StageProgress{}
	for rows.Next() {
		var s StageProgress
		if err = rows.Scan(&s.ID, &s.ReferralID, &s.HeroID, &s.Hero, &s.Driver, &s.Position, &s.TripTarget, &s.RewardKobo, &s.ApprovalMode, &s.CompletedTrips, &s.QualifiedAt, &s.CreditedAt, &s.ApprovedBy, &s.PaidAt, &s.PaymentReference, &s.DeadlineAt, &s.PayoutMode, &s.PayoutStatus, &s.PayoutID); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

func ProcessStages(ctx context.Context, tx pgx.Tx) error {
	// A separate stage row is the qualification/credit idempotency boundary.
	_, err := tx.Exec(ctx, `UPDATE hero_referral_stages SET qualified_at=now() WHERE id IN(
 SELECT s.id FROM hero_referral_stages s JOIN referral_attributions a ON a.id=s.referral_id JOIN users u ON u.id=a.invitee_id JOIN driver_eligibility e ON e.driver_id=u.id
 WHERE s.qualified_at IS NULL AND u.active AND 'driver'=ANY(u.roles) AND e.eligible_to_drive
 AND NOT EXISTS(SELECT 1 FROM simulation_participants sp WHERE sp.actor_user_id=u.id)
 AND `+ProgressSQL+`>=s.trip_target ORDER BY a.linked_at,s.position LIMIT 200 FOR UPDATE OF s SKIP LOCKED)`)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text FROM hero_referral_stages s WHERE s.qualified_at IS NOT NULL AND s.credited_at IS NULL AND s.approval_mode='automatic' ORDER BY s.referral_id,s.position LIMIT 200 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = creditStage(ctx, tx, id, nil); err != nil {
			return err
		}
	}
	return nil
}

func ApproveStages(ctx context.Context, tx pgx.Tx, ids []string, staff string) error {
	if len(ids) < 1 || len(ids) > 100 {
		return errors.New("Select between 1 and 100 stages")
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return ErrInvalid
		}
		if err := creditStage(ctx, tx, id, &staff); err != nil {
			return err
		}
	}
	return nil
}

func creditStage(ctx context.Context, tx pgx.Tx, id string, staff *string) error {
	var hero, mode string
	var amount int64
	var qualified, credited *time.Time
	err := tx.QueryRow(ctx, `SELECT a.hero_id::text,s.reward_kobo,s.approval_mode,s.qualified_at,s.credited_at FROM hero_referral_stages s JOIN referral_attributions a ON a.id=s.referral_id WHERE s.id=$1::uuid FOR UPDATE OF s`, id).Scan(&hero, &amount, &mode, &qualified, &credited)
	if err != nil {
		return err
	}
	if credited != nil {
		return nil
	}
	if qualified == nil {
		return errors.New("Stage has not qualified")
	}
	if staff == nil && mode != "automatic" {
		return errors.New("Manual approval required")
	}
	_, err = tx.Exec(ctx, `INSERT INTO heroes_wallet_entries(id,hero_id,stage_id,amount_kobo) VALUES($1::uuid,$2::uuid,$3::uuid,$4)`, uuid.NewString(), hero, id, amount)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE hero_referral_stages SET credited_at=now(),approved_by=$2::uuid WHERE id=$1::uuid`, id, staff)
	return err
}

// AllocateStagePayment confirms only fully paid, credited stages belonging to the
// paid Hero. The caller holds the Hero money lock and inserts payment atomically.
func AllocateStagePayment(ctx context.Context, tx pgx.Tx, hero, payment string, amount int64, ids []string) error {
	if len(ids) > 100 {
		return errors.New("Select up to 100 stages")
	}
	if len(ids) == 0 {
		// Unallocated legacy payments cannot consume earnings belonging to a stage.
		var legacy int64
		err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT sum(amount_kobo) FROM heroes_wallet_entries WHERE hero_id=$1::uuid AND stage_id IS NULL),0)-COALESCE((SELECT sum(p.amount_kobo) FROM heroes_manual_payments p WHERE p.hero_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM heroes_payment_reversals v WHERE v.payment_id=p.id) AND NOT EXISTS(SELECT 1 FROM hero_stage_payment_allocations pa WHERE pa.payment_id=p.id) AND p.id<>$2::uuid),0)`, hero, payment).Scan(&legacy)
		if err != nil {
			return err
		}
		if amount > legacy {
			return errors.New("Select the stages being paid")
		}
		return nil
	}
	sort.Strings(ids)
	var total int64
	seen := map[string]bool{}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil || seen[id] {
			return errors.New("Invalid or duplicate stage")
		}
		seen[id] = true
		var reward int64
		var credited *time.Time
		var paid bool
		err := tx.QueryRow(ctx, `SELECT s.reward_kobo,s.credited_at,EXISTS(SELECT 1 FROM hero_stage_payment_allocations pa WHERE pa.stage_id=s.id AND NOT EXISTS(SELECT 1 FROM heroes_payment_reversals v WHERE v.payment_id=pa.payment_id)) FROM hero_referral_stages s JOIN referral_attributions a ON a.id=s.referral_id WHERE s.id=$1::uuid AND a.hero_id=$2::uuid AND NOT EXISTS(SELECT 1 FROM hero_stage_payouts po WHERE po.stage_id=s.id AND po.id<>$3::uuid AND po.status NOT IN ('FAILED','EXPIRED','REVERSED','CANCELLED')) FOR UPDATE OF s`, id, hero, payment).Scan(&reward, &credited, &paid)
		if err != nil {
			return err
		}
		if credited == nil || paid {
			return errors.New("Stage is not payable")
		}
		total += reward
	}
	if total != amount {
		return errors.New("Payment must equal the selected stage rewards")
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `INSERT INTO hero_stage_payment_allocations(stage_id,payment_id)VALUES($1::uuid,$2::uuid)`, id, payment); err != nil {
			return err
		}
	}
	return nil
}
