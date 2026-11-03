CREATE TABLE heroes_bank_accounts (
 id UUID PRIMARY KEY, hero_id UUID NOT NULL REFERENCES heroes_accounts(id),
 bank_name TEXT NOT NULL CHECK(length(bank_name) BETWEEN 1 AND 100),
 account_name_ciphertext BYTEA NOT NULL, account_number_ciphertext BYTEA NOT NULL,
 account_number_masked TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE heroes_accounts ADD COLUMN payout_account_id UUID REFERENCES heroes_bank_accounts(id);
CREATE TABLE heroes_manual_payments (
 id UUID PRIMARY KEY, hero_id UUID NOT NULL REFERENCES heroes_accounts(id),
 bank_account_id UUID NOT NULL REFERENCES heroes_bank_accounts(id),
 amount_kobo BIGINT NOT NULL CHECK(amount_kobo>0), reference TEXT NOT NULL UNIQUE,
 paid_at TIMESTAMPTZ NOT NULL, recorded_by UUID NOT NULL REFERENCES admin_users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX heroes_manual_payments_owner ON heroes_manual_payments(hero_id,created_at DESC);
CREATE TABLE heroes_payment_reversals (
 payment_id UUID PRIMARY KEY REFERENCES heroes_manual_payments(id),
 reason TEXT NOT NULL CHECK(length(reason) BETWEEN 5 AND 500),
 recorded_by UUID NOT NULL REFERENCES admin_users(id),created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER heroes_bank_immutable BEFORE UPDATE OR DELETE ON heroes_bank_accounts FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
CREATE TRIGGER heroes_payment_immutable BEFORE UPDATE OR DELETE ON heroes_manual_payments FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
CREATE TRIGGER heroes_payment_reversal_immutable BEFORE UPDATE OR DELETE ON heroes_payment_reversals FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
