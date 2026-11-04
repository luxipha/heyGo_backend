// Package referrals owns immutable referral rules, signup attribution and approved rewards.
package referrals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/operatingcredits"
	"github.com/luxipha/heyGo_backend/shared/statutory"
)

var ErrUnavailable = errors.New("Referrals are unavailable")
var ErrIneligible = errors.New("Referral codes are for new accounts only")
var ErrConflict = errors.New("Your referrer is already confirmed")
var ErrInvalid = errors.New("Referral code not found")
var ErrStale = errors.New("Settings changed. Reload before saving")

type Rule struct {
	RewardKobo   int64 `json:"rewardKobo"`
	TripTarget   int   `json:"tripTarget"`
	DeadlineDays *int  `json:"deadlineDays"`
}
type Settings struct {
	Version        int64           `json:"version"`
	Enabled        bool            `json:"enabled"`
	DisabledNotice string          `json:"disabledNotice"`
	DriverHeadline string          `json:"driverHeadline"`
	DriverBody     string          `json:"driverBody"`
	RiderHeadline  string          `json:"riderHeadline"`
	RiderBody      string          `json:"riderBody"`
	Rules          map[string]Rule `json:"rules"`
}

var RuleKeys = []string{"hero_driver", "driver_driver", "driver_rider", "rider_rider"}

func (s Settings) Validate() error {
	if len(s.Rules) != len(RuleKeys) {
		return errors.New("Set all four reward rules")
	}
	for _, key := range RuleKeys {
		r, ok := s.Rules[key]
		if !ok || r.RewardKobo < 0 || r.RewardKobo > 100000000 || r.TripTarget < 1 || r.TripTarget > 10000 || r.DeadlineDays != nil && (*r.DeadlineDays < 1 || *r.DeadlineDays > 3650) {
			return fmt.Errorf("Invalid reward rule: %s", key)
		}
	}
	for _, v := range []struct {
		text     string
		min, max int
	}{{s.DisabledNotice, 1, 240}, {s.DriverHeadline, 1, 120}, {s.DriverBody, 0, 240}, {s.RiderHeadline, 1, 120}, {s.RiderBody, 0, 240}} {
		n := utf8.RuneCountInString(strings.TrimSpace(v.text))
		if n < v.min || n > v.max {
			return errors.New("Invalid banner or disabled notice")
		}
	}
	return nil
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Read(ctx context.Context, q querier) (Settings, error) {
	var s Settings
	var raw []byte
	err := q.QueryRow(ctx, `SELECT version,enabled,disabled_notice,driver_headline,driver_body,rider_headline,rider_body,rules FROM referral_settings ORDER BY version DESC LIMIT 1`).Scan(&s.Version, &s.Enabled, &s.DisabledNotice, &s.DriverHeadline, &s.DriverBody, &s.RiderHeadline, &s.RiderBody, &raw)
	if err == nil {
		err = json.Unmarshal(raw, &s.Rules)
	}
	return s, err
}
func Save(ctx context.Context, tx pgx.Tx, s Settings, staff string) (Settings, error) {
	if err := s.Validate(); err != nil {
		return Settings{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('referral-settings'))`); err != nil {
		return Settings{}, err
	}
	old, err := Read(ctx, tx)
	if err != nil {
		return Settings{}, err
	}
	if old.Version != s.Version {
		return Settings{}, ErrStale
	}
	raw, _ := json.Marshal(s.Rules)
	err = tx.QueryRow(ctx, `INSERT INTO referral_settings(enabled,disabled_notice,driver_headline,driver_body,rider_headline,rider_body,rules,created_by)VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8::uuid)RETURNING version`, s.Enabled, s.DisabledNotice, s.DriverHeadline, s.DriverBody, s.RiderHeadline, s.RiderBody, string(raw), staff).Scan(&s.Version)
	return s, err
}

type Referrer struct {
	ID   string `json:"-"`
	Name string `json:"name"`
	Code string `json:"code"`
	Role string `json:"role"`
}

func Lookup(ctx context.Context, q querier, code, target string) (Referrer, error) {
	var r Referrer
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) < 3 || len(code) > 32 || (target != "driver" && target != "rider") {
		return r, ErrInvalid
	}
	s, err := Read(ctx, q)
	if err != nil {
		return r, err
	}
	if !s.Enabled {
		return r, fmt.Errorf("%w: %s", ErrUnavailable, s.DisabledNotice)
	}
	err = q.QueryRow(ctx, `SELECT u.id::text,split_part(COALESCE(NULLIF(dp.display_name,''),NULLIF(rp.display_name,''),'Member'),' ',1),c.code,c.inviter_role FROM referral_codes c JOIN users u ON u.id=c.user_id LEFT JOIN driver_profiles dp ON dp.driver_id=u.id LEFT JOIN rider_profiles rp ON rp.rider_id=u.id WHERE c.code=$1 AND u.active AND (c.inviter_role='driver' OR $2='rider') AND NOT EXISTS(SELECT 1 FROM simulation_participants sp WHERE sp.actor_user_id=u.id)`, code, target).Scan(&r.ID, &r.Name, &r.Code, &r.Role)
	if errors.Is(err, pgx.ErrNoRows) && target == "driver" {
		err = q.QueryRow(ctx, `SELECT id::text,split_part(name,' ',1),referral_code,'hero' FROM heroes_accounts WHERE referral_code=$1 AND active`, code).Scan(&r.ID, &r.Name, &r.Code, &r.Role)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrInvalid
	}
	return r, err
}

// Claim runs before role registration. Account row locks and the shared legacy
// guard serialize all attribution paths. Replays never overwrite attribution.
func Claim(ctx context.Context, tx pgx.Tx, userID, code, target string) error {
	if target != "driver" && target != "rider" {
		return ErrInvalid
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('referral-settings'))`); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "referral-user:"+userID); err != nil {
		return err
	}
	var open, active bool
	var roles []string
	if err := tx.QueryRow(ctx, `SELECT referral_signup_open,active,roles FROM users WHERE id=$1::uuid FOR UPDATE`, userID).Scan(&open, &active, &roles); err != nil {
		return err
	}
	var confirmed string
	err := tx.QueryRow(ctx, `SELECT code FROM referral_attributions a JOIN referral_codes c ON c.user_id=a.inviter_id AND c.inviter_role=a.inviter_role WHERE a.invitee_id=$1::uuid UNION ALL SELECT h.referral_code FROM referral_attributions a JOIN heroes_accounts h ON h.id=a.hero_id WHERE a.invitee_id=$1::uuid UNION ALL SELECT h.referral_code FROM heroes_referrals r JOIN heroes_accounts h ON h.id=r.hero_id WHERE r.driver_id=$1::uuid`, userID).Scan(&confirmed)
	if err == nil {
		if confirmed == strings.ToUpper(strings.TrimSpace(code)) {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if !open || !active || len(roles) > 0 {
		return ErrIneligible
	}
	var used bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trips WHERE rider_id=$1::uuid OR assigned_driver_id=$1::uuid) OR EXISTS(SELECT 1 FROM simulation_participants WHERE actor_user_id=$1::uuid)`, userID).Scan(&used); err != nil {
		return err
	}
	if used {
		return ErrIneligible
	}
	r, err := Lookup(ctx, tx, code, target)
	if err != nil {
		return err
	}
	if r.ID == userID {
		return ErrInvalid
	}
	var same bool
	if r.Role == "hero" {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u JOIN heroes_accounts h ON lower(u.email)=h.email WHERE u.id=$1::uuid AND h.id=$2::uuid)`, userID, r.ID).Scan(&same)
	} else {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users a JOIN users b ON a.id=$1::uuid AND b.id=$2::uuid WHERE (a.email IS NOT NULL AND lower(a.email)=lower(b.email)) OR (a.phone_e164 IS NOT NULL AND a.phone_e164=b.phone_e164))`, userID, r.ID).Scan(&same)
	}
	if err != nil {
		return err
	}
	if same {
		return ErrInvalid
	}
	s, err := Read(ctx, tx)
	if err != nil {
		return err
	}
	rule := s.Rules[r.Role+"_"+target]
	var deadline *time.Time
	if rule.DeadlineDays != nil {
		v := time.Now().UTC().Add(time.Duration(*rule.DeadlineDays) * 24 * time.Hour)
		deadline = &v
	}
	var inviter, hero *string
	if r.Role == "hero" {
		hero = &r.ID
	} else {
		inviter = &r.ID
	}
	_, err = tx.Exec(ctx, `INSERT INTO referral_attributions(id,invitee_id,inviter_id,hero_id,inviter_role,invitee_role,settings_version,reward_kobo,trip_target,deadline_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10)`, uuid.NewString(), userID, inviter, hero, r.Role, target, s.Version, rule.RewardKobo, rule.TripTarget, deadline)
	return err
}
func OwnCode(ctx context.Context, pool *pgxpool.Pool, user, role string) (string, error) {
	if role != "driver" && role != "rider" {
		return "", ErrInvalid
	}
	code := "GO-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:12]
	_, err := pool.Exec(ctx, `INSERT INTO referral_codes(user_id,code,inviter_role) SELECT id,$2,$3 FROM users WHERE id=$1::uuid AND active AND $3=ANY(roles) AND NOT EXISTS(SELECT 1 FROM simulation_participants sp WHERE sp.actor_user_id=users.id) ON CONFLICT(user_id,inviter_role)DO NOTHING`, user, code, role)
	if err != nil {
		return "", err
	}
	err = pool.QueryRow(ctx, `SELECT code FROM referral_codes WHERE user_id=$1::uuid AND inviter_role=$2`, user, role).Scan(&code)
	return code, err
}

// Progress counts only real completed trips created after attribution, within the
// captured deadline, excluding self rides and simulation participants.
const ProgressSQL = `(SELECT count(*) FROM trips t WHERE t.status='completed' AND t.created_at>=a.linked_at AND (a.deadline_at IS NULL OR t.completed_at<=a.deadline_at) AND t.rider_id IS DISTINCT FROM t.assigned_driver_id AND ((a.invitee_role='driver' AND t.assigned_driver_id=a.invitee_id) OR (a.invitee_role='rider' AND t.rider_id=a.invitee_id)) AND NOT EXISTS(SELECT 1 FROM simulation_participants sp WHERE sp.actor_user_id=t.rider_id OR sp.actor_user_id=t.assigned_driver_id))`

func ProcessRewards(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE referral_attributions SET onboarded_at=now() WHERE id IN(SELECT a.id FROM referral_attributions a JOIN driver_eligibility e ON e.driver_id=a.invitee_id WHERE a.invitee_role='driver' AND a.onboarded_at IS NULL AND e.eligible_to_drive ORDER BY a.linked_at LIMIT 100 FOR UPDATE OF a SKIP LOCKED)`); err != nil {
		return err
	}

	rows, err := tx.Query(ctx, `SELECT a.id::text FROM referral_attributions a JOIN users u ON u.id=a.invitee_id LEFT JOIN driver_eligibility e ON e.driver_id=u.id WHERE a.qualified_at IS NULL AND a.reward_kobo>0 AND u.active AND a.invitee_role=ANY(u.roles) AND (a.invitee_role='rider' OR COALESCE(e.eligible_to_drive,false)) AND NOT EXISTS(SELECT 1 FROM simulation_participants sp WHERE sp.actor_user_id=u.id OR sp.actor_user_id=a.inviter_id) AND `+ProgressSQL+`>=a.trip_target ORDER BY a.linked_at LIMIT 100 FOR UPDATE OF a SKIP LOCKED`)
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
		if _, err = tx.Exec(ctx, `UPDATE referral_attributions SET qualified_at=now() WHERE id=$1::uuid`, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO referral_admin_notifications(referral_id)VALUES($1::uuid)ON CONFLICT DO NOTHING`, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Approve uses one transaction for the entire selected batch. Sorted row locks
// avoid overlapping-batch deadlocks; unique source keys make retries harmless.
func Approve(ctx context.Context, tx pgx.Tx, ids []string, staff string) error {
	if len(ids) < 1 || len(ids) > 100 {
		return errors.New("Select between 1 and 100 rewards")
	}
	sort.Strings(ids)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('referral-approval-batch'))`); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return ErrInvalid
		}
		var user, role string
		var hero *string
		var amount int64
		var qualified, credited *time.Time
		err := tx.QueryRow(ctx, `SELECT COALESCE(inviter_id::text,''),inviter_role,hero_id::text,reward_kobo,qualified_at,credited_at FROM referral_attributions WHERE id=$1::uuid FOR UPDATE`, id).Scan(&user, &role, &hero, &amount, &qualified, &credited)
		if err != nil {
			return err
		}
		if credited != nil {
			continue
		}
		if qualified == nil || amount <= 0 {
			return errors.New("Reward has not qualified")
		}
		source := "referral:" + id
		if role == "hero" {
			if hero == nil {
				return ErrInvalid
			}
			if _, err = tx.Exec(ctx, `INSERT INTO heroes_wallet_entries(id,hero_id,programme_referral_id,amount_kobo)VALUES($1::uuid,$2::uuid,$3::uuid,$4)`, uuid.NewString(), *hero, id, amount); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE referral_attributions SET credited_at=now(),approved_by=$2::uuid WHERE id=$1::uuid`, id, staff); err != nil {
				return err
			}
			continue
		}
		var active bool
		if err = tx.QueryRow(ctx, `SELECT active FROM users WHERE id=$1::uuid FOR SHARE`, user).Scan(&active); err != nil {
			return err
		}
		if !active {
			return errors.New("Referrer account is inactive")
		}
		if role == "driver" {
			if _, err = tx.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id)VALUES($1::uuid)ON CONFLICT DO NOTHING`, user); err != nil {
				return err
			}
			var balance int64
			if err = tx.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::uuid FOR UPDATE`, user).Scan(&balance); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details)VALUES($1::uuid,$2::uuid,'referral_reward',$3,$4,$5,jsonb_build_object('referralId',$6::text,'approvedBy',$7::text))`, uuid.NewString(), user, amount, balance+amount, source, id, staff); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=balance_kobo+$2,updated_at=now()WHERE driver_id=$1::uuid`, user, amount); err != nil {
				return err
			}
			balance += amount
			if err = statutory.RepayArrears(ctx, tx, user, "referral_reward", &balance); err != nil {
				return err
			}
			if err = operatingcredits.SettleCollectedDebts(ctx, tx, user, "referral_reward"); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `SELECT refresh_driver_operating_market($1::uuid)`, user); err != nil {
				return err
			}
		} else {
			if _, err = tx.Exec(ctx, `INSERT INTO rider_credit_accounts(rider_id)VALUES($1::uuid)ON CONFLICT DO NOTHING`, user); err != nil {
				return err
			}
			var balance int64
			if err = tx.QueryRow(ctx, `SELECT balance_kobo FROM rider_credit_accounts WHERE rider_id=$1::uuid FOR UPDATE`, user).Scan(&balance); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO rider_credit_entries(id,rider_id,source_key,delta_kobo,balance_after_kobo,kind)VALUES($1::uuid,$2::uuid,$3,$4,$5,'referral_reward')`, uuid.NewString(), user, source, amount, balance+amount); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE rider_credit_accounts SET balance_kobo=balance_kobo+$2,updated_at=now()WHERE rider_id=$1::uuid`, user, amount); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE referral_attributions SET credited_at=now(),approved_by=$2::uuid WHERE id=$1::uuid`, id, staff); err != nil {
			return err
		}
	}
	return nil
}

// SpendRideCredit applies non-withdrawable credit only to a completed trip's
// fare. The Driver receives the exact same amount in operational balance; debt
// collections and the full-fare earnings/statutory basis remain separate.
func SpendRideCredit(ctx context.Context, tx pgx.Tx, tripID string) (int64, error) {
	var rider, driver string
	var fare, applied int64
	err := tx.QueryRow(ctx, `SELECT t.rider_id::text,t.assigned_driver_id::text,ROUND(f.total_fare_minor)::bigint,t.ride_credit_kobo FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.id=$1::uuid AND t.status='completed' FOR UPDATE OF t`, tripID).Scan(&rider, &driver, &fare, &applied)
	if err != nil {
		return 0, err
	}
	if applied > 0 {
		return applied, nil
	}
	var available int64
	err = tx.QueryRow(ctx, `SELECT balance_kobo FROM rider_credit_accounts WHERE rider_id=$1::uuid`, rider).Scan(&available)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if available == 0 {
		return 0, nil
	}
	// Same ordering as reward approval, before acquiring any balance row lock.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('referral-approval-batch'))`); err != nil {
		return 0, err
	}
	if err = tx.QueryRow(ctx, `SELECT balance_kobo FROM rider_credit_accounts WHERE rider_id=$1::uuid FOR UPDATE`, rider).Scan(&available); err != nil {
		return 0, err
	}
	applied = min(available, fare)
	if applied == 0 {
		return 0, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE rider_credit_accounts SET balance_kobo=balance_kobo-$2,updated_at=now()WHERE rider_id=$1::uuid`, rider, applied); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO rider_credit_entries(id,rider_id,source_key,delta_kobo,balance_after_kobo,kind,trip_id)VALUES($1::uuid,$2::uuid,$3,$4,$5,'ride_payment',$6::uuid)`, uuid.NewString(), rider, "ride:"+tripID, -applied, available-applied, tripID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id)VALUES($1::uuid)ON CONFLICT DO NOTHING`, driver); err != nil {
		return 0, err
	}
	var balance int64
	if err = tx.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::uuid FOR UPDATE`, driver).Scan(&balance); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details)VALUES($1::uuid,$2::uuid,'rider_credit_payment',$3,$4,$5,jsonb_build_object('tripId',$6::text))`, uuid.NewString(), driver, applied, balance+applied, "ride-credit:"+tripID, tripID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=balance_kobo+$2,updated_at=now()WHERE driver_id=$1::uuid`, driver, applied); err != nil {
		return 0, err
	}
	balance += applied
	if err = statutory.RepayArrears(ctx, tx, driver, "rider_credit_payment", &balance); err != nil {
		return 0, err
	}
	if err = operatingcredits.SettleCollectedDebts(ctx, tx, driver, "rider_credit_payment"); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE trips SET ride_credit_kobo=$2 WHERE id=$1::uuid`, tripID, applied); err != nil {
		return 0, err
	}
	return applied, nil
}
