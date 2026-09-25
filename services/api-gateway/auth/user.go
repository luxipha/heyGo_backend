package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	sharedauth "github.com/cprakhar/uber-clone/shared/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidRole = errors.New("role must be rider or driver")
var ErrAccountDeactivated = errors.New("HeyGo account is deactivated")

type User struct {
	ID            string   `json:"id"`
	HumanID       string   `json:"humanId"`
	Email         string   `json:"-"`
	EmailVerified bool     `json:"-"`
	CasperSubject string   `json:"-"`
	Roles         []string `json:"roles"`
	Verified      bool     `json:"verified"`
	KYCTier       string   `json:"kycTier,omitempty"`
	TrustScore    *int     `json:"trustScore,omitempty"`
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
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (id, casper_id_user_id, human_id, roles, verified, kyc_tier, trust_score, created_at, updated_at)
		VALUES ($1, $2, $3, '{}'::TEXT[], $4, NULLIF($5, ''), $6, $7, $7)
		ON CONFLICT (human_id) DO UPDATE SET
			casper_id_user_id = EXCLUDED.casper_id_user_id,
			verified = EXCLUDED.verified,
			kyc_tier = EXCLUDED.kyc_tier,
			trust_score = EXCLUDED.trust_score,
			updated_at = EXCLUDED.updated_at
		WHERE users.active=TRUE
		RETURNING id::TEXT, human_id, roles, verified, COALESCE(kyc_tier, ''), trust_score`,
		uuid.New(), identity.Subject, identity.HumanID, identity.Verified, identity.KYCTier, identity.TrustScore, time.Now().UTC(),
	).Scan(&user.ID, &user.HumanID, &user.Roles, &user.Verified, &user.KYCTier, &user.TrustScore)
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
	return user, nil
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
	var user User
	err := s.pool.QueryRow(ctx, `
		UPDATE users
		SET roles = CASE WHEN $2 = ANY(roles) THEN roles ELSE array_append(roles, $2) END,
			updated_at = NOW()
		WHERE id = $1::UUID AND active=TRUE
		RETURNING id::TEXT, human_id, roles, verified, COALESCE(kyc_tier, ''), trust_score`, userID, role,
	).Scan(&user.ID, &user.HumanID, &user.Roles, &user.Verified, &user.KYCTier, &user.TrustScore)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, fmt.Errorf("user not found")
	}
	if err != nil {
		return User{}, fmt.Errorf("add user role: %w", err)
	}
	return user, nil
}

func (u User) HasRole(role string) bool { return slices.Contains(u.Roles, role) }
