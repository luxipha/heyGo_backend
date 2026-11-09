package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/services/payment-service/types"
	"github.com/luxipha/heyGo_backend/shared/monnifysettings"
)

type ConfiguredMonnify struct {
	Pool     *pgxpool.Pool
	Key      []byte
	Fallback *types.PaymentConfig
}

// Configuration is pinned by locally stored references; a callback cannot choose
// credentials using untrusted provider metadata. Existing environment orders
// retain their original environment configuration after Admin takes over.
func (m *ConfiguredMonnify) Config(ctx context.Context, reference string) (*types.PaymentConfig, error) {
	var version *int64
	known := false
	if reference != "" {
		e := m.Pool.QueryRow(ctx, `SELECT monnify_config_version FROM rider_trip_payments WHERE payment_reference=$1 OR transaction_reference=$1
  UNION ALL SELECT monnify_config_version FROM driver_operating_topups WHERE provider_reference=$1 OR provider_transaction_reference=$1
  UNION ALL SELECT monnify_config_version FROM rider_no_show_debt_payments WHERE provider_reference=$1 OR provider_transaction_reference=$1
  UNION ALL SELECT monnify_config_version FROM payments WHERE payment_reference=$1 OR transaction_reference=$1
  UNION ALL SELECT t.monnify_config_version FROM refund_requests r JOIN driver_operating_topups t ON t.id=r.topup_id WHERE r.refund_reference=$1 LIMIT 1`, reference).Scan(&version)
		if e == nil {
			known = true
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
	}
	if known && version == nil {
		return m.fallback()
	}
	v := int64(0)
	if version != nil {
		v = *version
	}
	s, e := monnifysettings.Read(ctx, m.Pool, v)
	if e != nil {
		return nil, e
	}
	if s.Version == 0 {
		return m.fallback()
	}
	if !s.Enabled {
		return nil, fmt.Errorf("Monnify is disabled")
	}
	if !s.Configured() {
		return nil, fmt.Errorf("Monnify configuration is incomplete")
	}
	config := &types.PaymentConfig{BaseURL: "https://sandbox.monnify.com"}
	if m.Fallback != nil {
		config.RedirectURL = m.Fallback.RedirectURL
	}
	if s.Environment == "live" {
		config.BaseURL = "https://api.monnify.com"
	}
	config.APIKey, e = monnifysettings.Open(m.Key, s.APIKey, "api-key")
	if e != nil {
		return nil, fmt.Errorf("Monnify settings cannot be decrypted")
	}
	config.SecretKey, e = monnifysettings.Open(m.Key, s.SecretKey, "secret-key")
	if e != nil {
		return nil, fmt.Errorf("Monnify settings cannot be decrypted")
	}
	config.ContractCode, e = monnifysettings.Open(m.Key, s.ContractCode, "contract-code")
	if e != nil {
		return nil, fmt.Errorf("Monnify settings cannot be decrypted")
	}
	return config, nil
}
func (m *ConfiguredMonnify) fallback() (*types.PaymentConfig, error) {
	if m.Fallback == nil || m.Fallback.APIKey == "" || m.Fallback.SecretKey == "" || m.Fallback.ContractCode == "" {
		return nil, fmt.Errorf("Monnify is not configured")
	}
	return m.Fallback, nil
}
func (m *ConfiguredMonnify) client(ctx context.Context, ref string) (*monnifyClient, error) {
	c, e := m.Config(ctx, ref)
	if e != nil {
		return nil, e
	}
	return NewMonnifyClient(c), nil
}
func (m *ConfiguredMonnify) Ping(ctx context.Context) error {
	c, e := m.client(ctx, "")
	if e != nil {
		return e
	}
	return c.Ping(ctx)
}
func (m *ConfiguredMonnify) CreatePaymentSession(ctx context.Context, ref string, amount int64, currency string, meta map[string]string) (*types.ProviderSession, error) {
	c, e := m.client(ctx, ref)
	if e != nil {
		return nil, e
	}
	return c.CreatePaymentSession(ctx, ref, amount, currency, meta)
}
func (m *ConfiguredMonnify) RecoverPaymentSession(ctx context.Context, ref string, amount int64, currency string) (*types.ProviderSession, error) {
	c, e := m.client(ctx, ref)
	if e != nil {
		return nil, e
	}
	return c.RecoverPaymentSession(ctx, ref, amount, currency)
}
func (m *ConfiguredMonnify) CreateOperatingTopupSession(ctx context.Context, amount int64, ref, email, driver string) (*types.ProviderSession, error) {
	c, e := m.client(ctx, ref)
	if e != nil {
		return nil, e
	}
	return c.CreateOperatingTopupSession(ctx, amount, ref, email, driver)
}
func (m *ConfiguredMonnify) CreateNoShowDebtPaymentSession(ctx context.Context, amount int64, ref, email, rider, debt string) (*types.ProviderSession, error) {
	c, e := m.client(ctx, ref)
	if e != nil {
		return nil, e
	}
	return c.CreateNoShowDebtPaymentSession(ctx, amount, ref, email, rider, debt)
}
func (m *ConfiguredMonnify) VerifyTransaction(ctx context.Context, ref string) (*VerifiedTransaction, error) {
	c, e := m.client(ctx, ref)
	if e != nil {
		return nil, e
	}
	return c.VerifyTransaction(ctx, ref)
}
func (m *ConfiguredMonnify) VerifyBankAccount(ctx context.Context, account, bank string) (*VerifiedBankAccount, error) {
	c, e := m.client(ctx, "")
	if e != nil {
		return nil, e
	}
	return c.VerifyBankAccount(ctx, account, bank)
}
func (m *ConfiguredMonnify) InitiateRefund(ctx context.Context, txn, ref string, amount int64, reason, account, bank string) (*RefundResult, error) {
	c, e := m.client(ctx, txn)
	if e != nil {
		return nil, e
	}
	return c.InitiateRefund(ctx, txn, ref, amount, reason, account, bank)
}
func (m *ConfiguredMonnify) RefundStatus(ctx context.Context, ref string) (*RefundResult, error) {
	c, e := m.client(ctx, ref)
	if e != nil {
		return nil, e
	}
	return c.RefundStatus(ctx, ref)
}
