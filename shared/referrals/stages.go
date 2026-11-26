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
	Kind             string     `json:"kind"`
	DriverID         string     `json:"driverId,omitempty"`
	DriverPhone      string     `json:"driverPhone,omitempty"`
	DriverNameSource string     `json:"driverNameSource,omitempty"`
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
	return ListStagePage(ctx, q, hero, offset, "all")
}

// Filtering happens before pagination so current targets cannot disappear at page boundaries.
func ListStagePage(ctx context.Context, q stageQuerier, hero string, offset int, view string) ([]StageProgress, error) {
	rows, err := q.Query(ctx, `SELECT s.id::text,a.id::text,a.hero_id::text,h.name,COALESCE(NULLIF(dp.display_name,''),CASE WHEN $3<>'public' THEN NULLIF(lead.name,'') END,'Driver'),s.position,s.trip_target,CASE WHEN $3='public' THEN s.reward_kobo ELSE hero_stage_amount(s.id) END,s.approval_mode,`+ProgressSQL+`,s.qualified_at,CASE WHEN $3='public' AND s.position=1 AND EXISTS(SELECT 1 FROM hero_referral_stages act WHERE act.referral_id=s.referral_id AND act.position=0 AND act.enabled AND act.credited_at IS NULL) THEN NULL ELSE s.credited_at END,s.approved_by::text,CASE WHEN $3='public' AND s.position=1 AND EXISTS(SELECT 1 FROM hero_referral_stages act WHERE act.referral_id=s.referral_id AND act.position=0 AND act.enabled AND NOT EXISTS(SELECT 1 FROM hero_stage_payment_allocations ap WHERE ap.stage_id=act.id AND NOT EXISTS(SELECT 1 FROM heroes_payment_reversals ar WHERE ar.payment_id=ap.payment_id))) THEN NULL ELSE p.paid_at END,p.reference,a.deadline_at,s.payout_mode,po.status,po.id::text,CASE WHEN s.position=0 THEN 'activation' ELSE 'trips' END,a.invitee_id::text,COALESCE(u.phone_e164,''),CASE WHEN NULLIF(dp.display_name,'') IS NOT NULL THEN 'profile' WHEN NULLIF(lead.name,'') IS NOT NULL THEN 'hero_lead' ELSE 'missing' END
 FROM hero_referral_stages s JOIN referral_attributions a ON a.id=s.referral_id JOIN heroes_accounts h ON h.id=a.hero_id LEFT JOIN driver_profiles dp ON dp.driver_id=a.invitee_id JOIN users u ON u.id=a.invitee_id
 LEFT JOIN LATERAL(SELECT l.name FROM heroes_driver_leads l WHERE l.hero_id=a.hero_id AND l.phone=u.phone_e164 ORDER BY l.created_at DESC LIMIT 1)lead ON true
 LEFT JOIN LATERAL(SELECT p.paid_at,p.reference FROM hero_stage_payment_allocations pa JOIN heroes_manual_payments p ON p.id=pa.payment_id WHERE pa.stage_id=s.id AND NOT EXISTS(SELECT 1 FROM heroes_payment_reversals v WHERE v.payment_id=p.id) ORDER BY p.created_at DESC LIMIT 1)p ON true
 LEFT JOIN LATERAL(SELECT status,id FROM hero_stage_payouts WHERE stage_id=s.id ORDER BY created_at DESC,id DESC LIMIT 1)po ON true
 WHERE s.enabled AND ($1='' OR a.hero_id::text=$1)
 AND ($3<>'public' OR s.position>0)
 AND ($3<>'current' OR (s.qualified_at IS NOT NULL AND p.paid_at IS NULL) OR
 (s.qualified_at IS NULL AND NOT EXISTS(SELECT 1 FROM hero_referral_stages prev WHERE prev.referral_id=s.referral_id AND prev.enabled AND prev.position<s.position AND prev.qualified_at IS NULL)))
 ORDER BY a.linked_at DESC,a.id,s.position LIMIT 201 OFFSET $2`, hero, offset, view)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []StageProgress{}
	for rows.Next() {
		var s StageProgress
		if err = rows.Scan(&s.ID, &s.ReferralID, &s.HeroID, &s.Hero, &s.Driver, &s.Position, &s.TripTarget, &s.RewardKobo, &s.ApprovalMode, &s.CompletedTrips, &s.QualifiedAt, &s.CreditedAt, &s.ApprovedBy, &s.PaidAt, &s.PaymentReference, &s.DeadlineAt, &s.PayoutMode, &s.PayoutStatus, &s.PayoutID, &s.Kind, &s.DriverID, &s.DriverPhone, &s.DriverNameSource); err != nil {
			return nil, err
		}
		if view == "public" {
			s.DriverID = ""
			s.DriverPhone = ""
			s.DriverNameSource = ""
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

func ProcessStages(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('referral-settings'))`); err != nil {
		return err
	}
	// A separate stage row is the qualification/credit idempotency boundary.
	_, err := tx.Exec(ctx, `UPDATE hero_referral_stages SET qualified_at=now() WHERE id IN(
 SELECT s.id FROM hero_referral_stages s JOIN referral_attributions a ON a.id=s.referral_id JOIN users u ON u.id=a.invitee_id JOIN driver_eligibility e ON e.driver_id=u.id
 WHERE s.enabled AND s.qualified_at IS NULL AND u.active AND 'driver'=ANY(u.roles)
 AND NOT EXISTS(SELECT 1 FROM simulation_participants sp WHERE sp.actor_user_id=u.id)
 AND ((s.position>0 AND e.eligible_to_drive AND `+ProgressSQL+`>=s.trip_target) OR (s.position=0 AND EXISTS(
 SELECT 1 FROM driver_first_online o WHERE o.driver_id=a.invitee_id AND o.first_online_at>=a.linked_at
 AND (a.deadline_at IS NULL OR o.first_online_at<=a.deadline_at)))) ORDER BY a.linked_at,s.position LIMIT 200 FOR UPDATE OF s SKIP LOCKED)`)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text FROM hero_referral_stages s WHERE s.enabled AND s.qualified_at IS NOT NULL AND s.credited_at IS NULL AND s.approval_mode='automatic' ORDER BY s.referral_id,s.position LIMIT 200 FOR UPDATE SKIP LOCKED`)
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
	err := tx.QueryRow(ctx, `SELECT a.hero_id::text,hero_stage_amount(s.id),s.approval_mode,s.qualified_at,s.credited_at FROM hero_referral_stages s JOIN referral_attributions a ON a.id=s.referral_id WHERE s.id=$1::uuid AND s.enabled FOR UPDATE OF s`, id).Scan(&hero, &amount, &mode, &qualified, &credited)
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
		err := tx.QueryRow(ctx, `SELECT hero_stage_amount(s.id),s.credited_at,EXISTS(SELECT 1 FROM hero_stage_payment_allocations pa WHERE pa.stage_id=s.id AND NOT EXISTS(SELECT 1 FROM heroes_payment_reversals v WHERE v.payment_id=pa.payment_id)) FROM hero_referral_stages s JOIN referral_attributions a ON a.id=s.referral_id WHERE s.id=$1::uuid AND a.hero_id=$2::uuid AND NOT EXISTS(SELECT 1 FROM hero_stage_payouts po WHERE po.stage_id=s.id AND po.id<>$3::uuid AND po.status NOT IN ('FAILED','EXPIRED','REVERSED','CANCELLED')) FOR UPDATE OF s`, id, hero, payment).Scan(&reward, &credited, &paid)
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
