-- Money policies and statutory rules have no seeded values. Two different
-- staff accounts are required before a version can become approved.
CREATE TABLE IF NOT EXISTS operating_balance_policies (
    id UUID PRIMARY KEY,
    market_code TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    minimum_kobo BIGINT NOT NULL CHECK (minimum_kobo >= 0),
    warning_kobo BIGINT NOT NULL CHECK (warning_kobo >= minimum_kobo),
    allow_negative BOOLEAN NOT NULL,
    maximum_negative_kobo BIGINT NOT NULL CHECK (maximum_negative_kobo >= 0),
    status TEXT NOT NULL CHECK (status IN ('draft', 'approved')),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_until TIMESTAMPTZ,
    created_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    approved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (market_code, version),
    CHECK (allow_negative OR maximum_negative_kobo = 0),
    CHECK (effective_until IS NULL OR effective_until > effective_from),
    CHECK ((approved_by IS NULL) = (approved_at IS NULL)),
    CHECK ((status = 'approved') = (approved_by IS NOT NULL)),
    CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE INDEX IF NOT EXISTS operating_balance_policies_active_idx
    ON operating_balance_policies (market_code, status, effective_from, effective_until);

CREATE TABLE IF NOT EXISTS operating_balance_policy_retirements (
    id UUID PRIMARY KEY,
    policy_id UUID NOT NULL REFERENCES operating_balance_policies(id),
    effective_until TIMESTAMPTZ NOT NULL,
    requested_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at TIMESTAMPTZ,
    CHECK ((approved_by IS NULL) = (approved_at IS NULL)),
    CHECK (approved_by IS NULL OR approved_by <> requested_by)
);
CREATE UNIQUE INDEX IF NOT EXISTS operating_balance_policy_pending_retirement_idx
    ON operating_balance_policy_retirements(policy_id) WHERE status='pending';

CREATE TABLE IF NOT EXISTS driver_operating_accounts (
    driver_id UUID PRIMARY KEY REFERENCES users(id),
    balance_kobo BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Each confirmed credit/debit is a signed, append-only ledger entry. The
-- account balance is updated in the same transaction after locking its row.
CREATE TABLE IF NOT EXISTS driver_operating_entries (
    id UUID PRIMARY KEY,
    driver_id UUID NOT NULL REFERENCES driver_operating_accounts(driver_id),
    kind TEXT NOT NULL CHECK (kind IN ('topup', 'statutory_charge', 'reversal')),
    delta_kobo BIGINT NOT NULL CHECK (delta_kobo <> 0),
    balance_after_kobo BIGINT NOT NULL,
    source_key TEXT NOT NULL UNIQUE,
    details JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((kind = 'topup' AND delta_kobo > 0) OR
           (kind = 'statutory_charge' AND delta_kobo < 0) OR
           kind = 'reversal')
);
CREATE INDEX IF NOT EXISTS driver_operating_entries_driver_idx
    ON driver_operating_entries (driver_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS driver_operating_topups (
    id UUID PRIMARY KEY,
    driver_id UUID NOT NULL REFERENCES users(id),
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo > 0),
    provider TEXT NOT NULL,
    provider_reference TEXT UNIQUE,
    checkout_url TEXT,
    status TEXT NOT NULL CHECK (status IN ('pending', 'succeeded', 'failed')),
    confirmed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((status = 'succeeded') = (confirmed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS driver_operating_topups_driver_idx
    ON driver_operating_topups (driver_id, created_at DESC);

-- Rule configuration is deliberately inert until an approved version is
-- effective. No VAT rate, levy amount, or charge bearer is hard-coded here.
CREATE TABLE IF NOT EXISTS statutory_charge_rules (
    id UUID PRIMARY KEY,
    ledger_code TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    name TEXT NOT NULL,
    authority TEXT NOT NULL,
    jurisdiction TEXT NOT NULL,
    calculation_type TEXT NOT NULL CHECK (calculation_type IN ('fixed', 'percentage', 'tiered', 'formula')),
    calculation_base TEXT NOT NULL,
    calculation_config JSONB NOT NULL,
    country_code TEXT,
    region_code TEXT,
    market_code TEXT,
    regulatory_tag TEXT,
    vehicle_package TEXT,
    bearer TEXT NOT NULL CHECK (bearer IN ('driver', 'rider', 'platform')),
    funding_source TEXT NOT NULL CHECK (funding_source IN ('operating_balance', 'rider_payment', 'platform')),
    minimum_kobo BIGINT CHECK (minimum_kobo >= 0),
    maximum_kobo BIGINT CHECK (maximum_kobo >= 0),
    status TEXT NOT NULL CHECK (status IN ('draft', 'approved')),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_until TIMESTAMPTZ,
    reference TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    created_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    approved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (ledger_code, version),
    CHECK (maximum_kobo IS NULL OR minimum_kobo IS NULL OR maximum_kobo >= minimum_kobo),
    CHECK (effective_until IS NULL OR effective_until > effective_from),
    CHECK ((approved_by IS NULL) = (approved_at IS NULL)),
    CHECK ((status = 'approved') = (approved_by IS NOT NULL)),
    CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE INDEX IF NOT EXISTS statutory_charge_rules_active_idx
    ON statutory_charge_rules (status, effective_from, effective_until, market_code, regulatory_tag);

CREATE OR REPLACE FUNCTION protect_approved_finance_config() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status = 'approved' THEN
            RAISE EXCEPTION 'approved finance versions cannot be deleted';
        END IF;
        RETURN OLD;
    END IF;
    IF OLD.status = 'approved' THEN
        IF (to_jsonb(NEW) - 'effective_until') IS DISTINCT FROM (to_jsonb(OLD) - 'effective_until') THEN
            RAISE EXCEPTION 'approved finance versions are immutable';
        END IF;
        IF NEW.effective_until IS DISTINCT FROM OLD.effective_until
           AND (OLD.effective_until IS NOT NULL OR NEW.effective_until IS NULL OR NEW.effective_until < NOW()) THEN
            RAISE EXCEPTION 'approved finance versions may only be retired once, now or in the future';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER operating_balance_policies_protect_approved
    BEFORE UPDATE OR DELETE ON operating_balance_policies
    FOR EACH ROW EXECUTE FUNCTION protect_approved_finance_config();
CREATE TRIGGER statutory_charge_rules_protect_approved
    BEFORE UPDATE OR DELETE ON statutory_charge_rules
    FOR EACH ROW EXECUTE FUNCTION protect_approved_finance_config();

-- A trip/rule pair is assessed once. The rule snapshot supports later audits
-- even if a new version supersedes it.
CREATE TABLE IF NOT EXISTS trip_statutory_charges (
    id UUID PRIMARY KEY,
    trip_id UUID NOT NULL REFERENCES trips(id),
    rule_id UUID NOT NULL REFERENCES statutory_charge_rules(id),
    driver_id UUID NOT NULL REFERENCES users(id),
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo >= 0),
    bearer TEXT NOT NULL,
    funding_source TEXT NOT NULL,
    rule_snapshot JSONB NOT NULL,
    operating_entry_id UUID UNIQUE REFERENCES driver_operating_entries(id),
    assessed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (trip_id, rule_id)
);

CREATE OR REPLACE FUNCTION protect_financial_ledger() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'financial ledger records are append only';
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER driver_operating_entries_append_only
    BEFORE UPDATE OR DELETE ON driver_operating_entries
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();
CREATE TRIGGER trip_statutory_charges_append_only
    BEFORE UPDATE OR DELETE ON trip_statutory_charges
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();
