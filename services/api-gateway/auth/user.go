package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
)

var ErrInvalidRole = errors.New("role must be rider or driver")
var ErrAccountDeactivated = errors.New("HeyGo account is deactivated")

type User struct {
	ID            string         `json:"id"`
	HumanID       string         `json:"-"`
	Phone         string         `json:"phone"`
	PhoneVerified bool           `json:"phoneVerified"`
	Name          string         `json:"name,omitempty"`
	RiderName     string         `json:"riderDisplayName"`
	Email         string         `json:"email,omitempty"`
	EmailVerified bool           `json:"emailVerified"`
	CasperSubject string         `json:"-"`
	Roles         []string       `json:"roles"`
	Verified      bool           `json:"verified"`
	KYCTier       string         `json:"kycTier,omitempty"`
	ProviderTrust *TrustSnapshot `json:"providerTrust,omitempty"`
	ConsumerTrust *TrustSnapshot `json:"consumerTrust,omitempty"`
}

type TrustSnapshot struct {
	Score         int       `json:"score"`
	Status        string    `json:"status"`
	CalculatedAt  time.Time `json:"calculatedAt"`
	PolicyVersion string    `json:"policyVersion"`
}

type UserStore interface {
	UpsertIdentity(context.Context, sharedauth.Identity) (User, error)
	AddRole(context.Context, string, string) (User, error)
}

type PostgresUserStore struct{ pool *pgxpool.Pool }

func NewPostgresUserStore(pool *pgxpool.Pool) *PostgresUserStore {
	return &PostgresUserStore{pool: pool}
}

func (s *PostgresUserStore) UpsertIdentity(ctx context.Context, identity sharedauth.Identity) (User, error) {
	var blocked bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deactivated_casperid_identities WHERE subject_hash=$1 OR human_id_hash=$2)`, identityHash(identity.Subject), identityHash(identity.HumanID)).Scan(&blocked); err != nil {
		return User{}, fmt.Errorf("check deactivated CasperID identity: %w", err)
	}
	if blocked {
		return User{}, ErrAccountDeactivated
	}
	var user User
	var providerScore, consumerScore *int
	var providerStatus, consumerStatus string
	var trustUpdatedAt *time.Time
	var trustPolicy string
	var storedProviderScore, storedConsumerScore *int
	if identity.TrustCapacity != nil && !identity.TrustCapacity.CalculatedAt.IsZero() {
		trustUpdatedAt = &identity.TrustCapacity.CalculatedAt
		if identity.TrustCapacity.Provider != nil {
			providerScore = &identity.TrustCapacity.Provider.Score
			providerStatus = identity.TrustCapacity.Provider.Status
			trustPolicy = identity.TrustCapacity.Provider.PolicyVersion
		}
		if identity.TrustCapacity.Consumer != nil {
			consumerScore = &identity.TrustCapacity.Consumer.Score
			consumerStatus = identity.TrustCapacity.Consumer.Status
			if trustPolicy == "" {
				trustPolicy = identity.TrustCapacity.Consumer.PolicyVersion
			}
		}
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (id, casper_id_user_id, human_id, roles, verified, kyc_tier, provider_trust_score, consumer_trust_score, provider_trust_status, consumer_trust_status, trust_score_updated_at, trust_score_policy_version, created_at, updated_at)
		VALUES ($1, $2, $3, '{}'::TEXT[], $4, NULLIF($5, ''), $6, $7, NULLIF($8, ''), NULLIF($9, ''), $10, NULLIF($11, ''), $12, $12)
		ON CONFLICT (human_id) DO UPDATE SET
			casper_id_user_id = EXCLUDED.casper_id_user_id,
			verified = EXCLUDED.verified,
			kyc_tier = EXCLUDED.kyc_tier,
			provider_trust_score = COALESCE(EXCLUDED.provider_trust_score, users.provider_trust_score),
			consumer_trust_score = COALESCE(EXCLUDED.consumer_trust_score, users.consumer_trust_score),
			provider_trust_status = COALESCE(EXCLUDED.provider_trust_status, users.provider_trust_status),
			consumer_trust_status = COALESCE(EXCLUDED.consumer_trust_status, users.consumer_trust_status),
			trust_score_updated_at = COALESCE(EXCLUDED.trust_score_updated_at, users.trust_score_updated_at),
			trust_score_policy_version = COALESCE(EXCLUDED.trust_score_policy_version, users.trust_score_policy_version),
			updated_at = EXCLUDED.updated_at
		WHERE users.active=TRUE
		RETURNING id::TEXT, COALESCE(human_id,''), roles, verified, COALESCE(kyc_tier, ''), provider_trust_score, consumer_trust_score, COALESCE(provider_trust_status,''), COALESCE(consumer_trust_status,''), trust_score_updated_at, COALESCE(trust_score_policy_version, '')`,
		uuid.New(), identity.Subject, identity.HumanID, identity.Verified, identity.KYCTier, providerScore, consumerScore, providerStatus, consumerStatus, trustUpdatedAt, trustPolicy, time.Now().UTC(),
	).Scan(&user.ID, &user.HumanID, &user.Roles, &user.Verified, &user.KYCTier, &storedProviderScore, &storedConsumerScore, &providerStatus, &consumerStatus, &trustUpdatedAt, &trustPolicy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var active bool
			lookupErr := s.pool.QueryRow(ctx, `SELECT active FROM users WHERE human_id=$1`, identity.HumanID).Scan(&active)
			if lookupErr == nil && !active {
				return User{}, ErrAccountDeactivated
			}
		}
		return User{}, fmt.Errorf("upsert authenticated user: %w", err)
	}
	user.Email = identity.Email
	user.EmailVerified = identity.EmailVerified
	user.CasperSubject = identity.Subject
	user.Name = identity.Name
	if user.HasRole("rider") {
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT display_name FROM rider_profiles WHERE rider_id=$1::UUID),'')`, user.ID).Scan(&user.RiderName); err != nil {
			return User{}, fmt.Errorf("read rider name: %w", err)
		}
		if !user.HasRole("driver") {
			user.Name = user.RiderName
		}
	}
	if storedProviderScore != nil {
		user.ProviderTrust = &TrustSnapshot{Score: *storedProviderScore, Status: providerStatus, CalculatedAt: trustUpdatedAtOrZero(trustUpdatedAt), PolicyVersion: trustPolicy}
	}
	if storedConsumerScore != nil {
		user.ConsumerTrust = &TrustSnapshot{Score: *storedConsumerScore, Status: consumerStatus, CalculatedAt: trustUpdatedAtOrZero(trustUpdatedAt), PolicyVersion: trustPolicy}
	}
	return user, nil
}

func trustUpdatedAtOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func IdentityHash(value string) string { return identityHash(value) }

func identityHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *PostgresUserStore) AddRole(ctx context.Context, userID, role string) (User, error) {
	if role != "rider" && role != "driver" {
		return User{}, ErrInvalidRole
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin role registration: %w", err)
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `
		UPDATE users
		SET roles = CASE WHEN $2 = ANY(roles) THEN roles ELSE array_append(roles, $2) END,
			updated_at = NOW()
		WHERE id = $1::UUID AND active=TRUE`, userID, role)
	if err != nil {
		return User{}, fmt.Errorf("add user role: %w", err)
	}
	if result.RowsAffected() == 0 {
		return User{}, fmt.Errorf("user not found")
	}
	if role == "driver" {
		if _, err := tx.Exec(ctx, `INSERT INTO driver_profiles(driver_id) VALUES($1::UUID) ON CONFLICT DO NOTHING`, userID); err != nil {
			return User{}, fmt.Errorf("initialize driver profile: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit role registration: %w", err)
	}
	return s.ByID(ctx, userID)
}

func (s *PostgresUserStore) FindOrCreateByVerifiedPhone(ctx context.Context, phone string) (User, error) {
	var blocked bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deactivated_phone_identities WHERE phone_hash=$1)`, identityHash(phone)).Scan(&blocked); err != nil {
		return User{}, fmt.Errorf("check deactivated phone identity: %w", err)
	}
	if blocked {
		return User{}, ErrAccountDeactivated
	}
	id := uuid.NewString()
	_, err := s.pool.Exec(ctx, `INSERT INTO users(id,phone_e164,phone_verified_at,roles,created_at,updated_at)
		VALUES($1::UUID,$2,NOW(),'{}'::TEXT[],NOW(),NOW())
		ON CONFLICT(phone_e164) WHERE phone_e164 IS NOT NULL DO UPDATE
		SET phone_verified_at=COALESCE(users.phone_verified_at,NOW()),updated_at=NOW()
		WHERE users.active=TRUE`, id, phone)
	if err != nil {
		return User{}, fmt.Errorf("create phone user: %w", err)
	}
	return s.ByPhone(ctx, phone)
}

func (s *PostgresUserStore) ByPhone(ctx context.Context, phone string) (User, error) {
	return s.readUser(ctx, `u.phone_e164=$1`, phone)
}

func (s *PostgresUserStore) ByID(ctx context.Context, id string) (User, error) {
	return s.readUser(ctx, `u.id=$1::UUID`, id)
}

func (s *PostgresUserStore) readUser(ctx context.Context, predicate string, value any) (User, error) {
	var user User
	var phoneVerifiedAt *time.Time
	var providerScore, consumerScore *int
	var providerStatus, consumerStatus string
	var trustUpdatedAt *time.Time
	var trustPolicy string
	err := s.pool.QueryRow(ctx, `SELECT u.id::TEXT,COALESCE(u.human_id,''),COALESCE(u.casper_id_user_id,''),COALESCE(u.phone_e164,''),u.phone_verified_at,
		COALESCE(u.email,''),u.email_verified,u.roles,u.verified,COALESCE(u.kyc_tier,''),
		u.provider_trust_score,u.consumer_trust_score,COALESCE(u.provider_trust_status,''),COALESCE(u.consumer_trust_status,''),
		u.trust_score_updated_at,COALESCE(u.trust_score_policy_version,''),
		CASE WHEN 'rider'=ANY(u.roles) AND NOT ('driver'=ANY(u.roles)) THEN COALESCE((SELECT p.display_name FROM rider_profiles p WHERE p.rider_id=u.id),'')
		ELSE COALESCE((SELECT p.display_name FROM driver_profiles p WHERE p.driver_id=u.id),'') END,
		COALESCE((SELECT p.display_name FROM rider_profiles p WHERE p.rider_id=u.id),'')
		FROM users u WHERE `+predicate+` AND u.active=TRUE`, value).Scan(
		&user.ID, &user.HumanID, &user.CasperSubject, &user.Phone, &phoneVerifiedAt, &user.Email, &user.EmailVerified,
		&user.Roles, &user.Verified, &user.KYCTier, &providerScore, &consumerScore,
		&providerStatus, &consumerStatus, &trustUpdatedAt, &trustPolicy, &user.Name, &user.RiderName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrAccountDeactivated
	}
	if err != nil {
		return User{}, fmt.Errorf("read authenticated user: %w", err)
	}
	user.PhoneVerified = phoneVerifiedAt != nil
	if providerScore != nil {
		user.ProviderTrust = &TrustSnapshot{Score: *providerScore, Status: providerStatus, CalculatedAt: trustUpdatedAtOrZero(trustUpdatedAt), PolicyVersion: trustPolicy}
	}
	if consumerScore != nil {
		user.ConsumerTrust = &TrustSnapshot{Score: *consumerScore, Status: consumerStatus, CalculatedAt: trustUpdatedAtOrZero(trustUpdatedAt), PolicyVersion: trustPolicy}
	}
	return user, nil
}

func (u User) HasRole(role string) bool { return slices.Contains(u.Roles, role) }
