package rideridentity

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

var ErrContactVerificationRequired = errors.New("rider email and phone verification required")

type Reader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}
type Readiness struct {
	EmailVerified bool `json:"emailVerified"`
	PhoneVerified bool `json:"phoneVerified"`
}

func Read(ctx context.Context, q Reader, id string) (out Readiness, err error) {
	err = q.QueryRow(ctx, `SELECT email_verified AND COALESCE(email,'')<>'',phone_verified_at IS NOT NULL AND COALESCE(phone_e164,'') ~ '^\+[1-9][0-9]{7,14}$' FROM users WHERE id=$1::uuid AND active AND 'rider'=ANY(roles)`, id).Scan(&out.EmailVerified, &out.PhoneVerified)
	return
}
func Require(ctx context.Context, q Reader, id string) error {
	state, e := Read(ctx, q, id)
	if e != nil {
		return e
	}
	if !state.EmailVerified || !state.PhoneVerified {
		return ErrContactVerificationRequired
	}
	return nil
}
