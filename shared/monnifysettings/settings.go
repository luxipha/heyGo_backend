package monnifysettings

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

type Settings struct {
	Version             int64  `json:"version"`
	Enabled             bool   `json:"enabled"`
	TripPaymentsEnabled bool   `json:"tripPaymentsEnabled"`
	Environment         string `json:"environment"`
	APIKey              []byte `json:"-"`
	SecretKey           []byte `json:"-"`
	ContractCode        []byte `json:"-"`
}
type Queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Read(ctx context.Context, q Queryer, version int64) (Settings, error) {
	s := Settings{Environment: "sandbox"}
	where := "ORDER BY version DESC LIMIT 1"
	args := []any{}
	if version > 0 {
		where = "WHERE version=$1"
		args = append(args, version)
	}
	e := q.QueryRow(ctx, `SELECT version,enabled,trip_payments_enabled,environment,api_key_ciphertext,secret_key_ciphertext,contract_code_ciphertext FROM admin_monnify_settings `+where, args...).Scan(&s.Version, &s.Enabled, &s.TripPaymentsEnabled, &s.Environment, &s.APIKey, &s.SecretKey, &s.ContractCode)
	if errors.Is(e, pgx.ErrNoRows) && version == 0 {
		return s, nil
	}
	return s, e
}
func (s Settings) Configured() bool {
	return len(s.APIKey) > 0 && len(s.SecretKey) > 0 && len(s.ContractCode) > 0
}
func Seal(key []byte, value, purpose string) ([]byte, error) {
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	return append(nonce, a.Seal(nil, nonce, []byte(value), []byte("heygo-monnify-"+purpose+"-v1"))...), nil
}
func Open(key, sealed []byte, purpose string) (string, error) {
	block, e := aes.NewCipher(key)
	if e != nil {
		return "", e
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	if len(sealed) < a.NonceSize()+a.Overhead() {
		return "", fmt.Errorf("invalid encrypted Monnify credential")
	}
	raw, e := a.Open(nil, sealed[:a.NonceSize()], sealed[a.NonceSize():], []byte("heygo-monnify-"+purpose+"-v1"))
	return string(raw), e
}
