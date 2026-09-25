-- No-show fees are configured per market, reviewed by two staff accounts,
-- collected from the rider on a later directly settled trip, and transferred
-- between Operating Balances when the collecting driver confirms receipt.
CREATE TABLE IF NOT EXISTS no_show_policies (
    id UUID PRIMARY KEY,
    market_code TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    wait_minutes INTEGER NOT NULL CHECK (wait_minutes BETWEEN 1 AND 180),
    fee_kobo BIGINT NOT NULL CHECK (fee_kobo > 0),
    status TEXT NOT NULL CHECK (status IN ('draft','approved')),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_until TIMESTAMPTZ,
    created_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    approved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(market_code,version),
    CHECK (effective_until IS NULL OR effective_until > effective_from),
    CHECK ((approved_by IS NULL) = (approved_at IS NULL)),
    CHECK ((status = 'approved') = (approved_by IS NOT NULL AND approved_at IS NOT NULL)),
    CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE INDEX IF NOT EXISTS no_show_policies_active_idx
    ON no_show_policies(market_code,effective_from,effective_until) WHERE status='approved';
CREATE TRIGGER no_show_policies_protect_approved
    BEFORE UPDATE OR DELETE ON no_show_policies
    FOR EACH ROW EXECUTE FUNCTION protect_approved_finance_config();
CREATE TABLE IF NOT EXISTS no_show_policy_retirements (
    id UUID PRIMARY KEY,
    policy_id UUID NOT NULL REFERENCES no_show_policies(id),
    effective_until TIMESTAMPTZ NOT NULL,
    requested_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    status TEXT NOT NULL CHECK (status IN ('pending','approved')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at TIMESTAMPTZ,
    CHECK ((approved_by IS NULL) = (approved_at IS NULL)),
    CHECK ((status = 'approved') = (approved_by IS NOT NULL AND approved_at IS NOT NULL)),
    CHECK (approved_by IS NULL OR approved_by <> requested_by)
);
CREATE UNIQUE INDEX IF NOT EXISTS no_show_policy_pending_retirement_idx
    ON no_show_policy_retirements(policy_id) WHERE status='pending';

CREATE TABLE IF NOT EXISTS driver_no_show_claims (
    id UUID PRIMARY KEY,
    trip_id UUID NOT NULL UNIQUE REFERENCES trips(id),
    driver_id UUID NOT NULL REFERENCES users(id),
    rider_id UUID NOT NULL REFERENCES users(id),
    market_code TEXT NOT NULL,
    policy_id UUID NOT NULL REFERENCES no_show_policies(id),
    fee_kobo BIGINT NOT NULL CHECK (fee_kobo > 0),
    arrived_at TIMESTAMPTZ NOT NULL,
    eligible_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','settled')),
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_by UUID REFERENCES admin_users(id),
    reviewed_at TIMESTAMPTZ,
    review_note TEXT,
    CHECK ((reviewed_by IS NULL) = (reviewed_at IS NULL)),
    CHECK ((status IN ('approved','rejected','settled')) = (reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS driver_no_show_claims_review_idx
    ON driver_no_show_claims(status,submitted_at DESC);
CREATE INDEX IF NOT EXISTS driver_no_show_claims_driver_idx
    ON driver_no_show_claims(driver_id,submitted_at DESC);

CREATE TABLE IF NOT EXISTS rider_no_show_debts (
    id UUID PRIMARY KEY,
    claim_id UUID NOT NULL UNIQUE REFERENCES driver_no_show_claims(id),
    rider_id UUID NOT NULL REFERENCES users(id),
    beneficiary_driver_id UUID NOT NULL REFERENCES users(id),
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo > 0),
    status TEXT NOT NULL CHECK (status IN ('due','settled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    settled_at TIMESTAMPTZ,
    CHECK ((status='settled')=(settled_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS rider_no_show_debts_due_idx
    ON rider_no_show_debts(rider_id,created_at,id) WHERE status='due';

CREATE TABLE IF NOT EXISTS trip_no_show_debt_settlements (
    trip_id UUID NOT NULL REFERENCES trips(id),
    debt_id UUID NOT NULL REFERENCES rider_no_show_debts(id),
    beneficiary_driver_id UUID NOT NULL REFERENCES users(id),
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo > 0),
    status TEXT NOT NULL CHECK (status IN ('pending','settled','released')),
    settled_at TIMESTAMPTZ,
    PRIMARY KEY(trip_id,debt_id),
    CHECK ((status='settled')=(settled_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS trip_no_show_debt_one_open_allocation_idx
    ON trip_no_show_debt_settlements(debt_id) WHERE status IN ('pending','settled');
CREATE INDEX IF NOT EXISTS trip_no_show_debt_settlements_trip_idx
    ON trip_no_show_debt_settlements(trip_id,status);

CREATE TABLE IF NOT EXISTS operating_balance_transfers (
    id UUID PRIMARY KEY,
    debt_id UUID NOT NULL UNIQUE REFERENCES rider_no_show_debts(id),
    trip_id UUID NOT NULL REFERENCES trips(id),
    from_driver_id UUID NOT NULL REFERENCES users(id),
    to_driver_id UUID NOT NULL REFERENCES users(id),
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo > 0),
    from_entry_id UUID,
    to_entry_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE ride_fares ADD COLUMN IF NOT EXISTS no_show_debt_kobo BIGINT NOT NULL DEFAULT 0 CHECK (no_show_debt_kobo >= 0);
ALTER TABLE trips ADD COLUMN IF NOT EXISTS no_show_debt_kobo BIGINT NOT NULL DEFAULT 0 CHECK (no_show_debt_kobo >= 0);
ALTER TABLE trip_settlements ADD COLUMN IF NOT EXISTS fare_amount_kobo BIGINT NOT NULL DEFAULT 0 CHECK (fare_amount_kobo >= 0);
ALTER TABLE trip_settlements ADD COLUMN IF NOT EXISTS no_show_debt_kobo BIGINT NOT NULL DEFAULT 0 CHECK (no_show_debt_kobo >= 0);

ALTER TABLE driver_operating_entries DROP CONSTRAINT IF EXISTS driver_operating_entries_kind_check;
ALTER TABLE driver_operating_entries ADD CONSTRAINT driver_operating_entries_kind_check
    CHECK (kind IN ('topup','statutory_charge','reversal','no_show_transfer'));
ALTER TABLE driver_operating_entries DROP CONSTRAINT IF EXISTS driver_operating_entries_check;
ALTER TABLE driver_operating_entries ADD CONSTRAINT driver_operating_entries_sign_check
    CHECK ((kind='topup' AND delta_kobo>0) OR
           (kind='statutory_charge' AND delta_kobo<0) OR
           (kind='no_show_transfer' AND delta_kobo<>0) OR kind='reversal');
ALTER TABLE operating_balance_transfers
    ADD CONSTRAINT operating_balance_transfers_from_entry_fk FOREIGN KEY(from_entry_id) REFERENCES driver_operating_entries(id),
    ADD CONSTRAINT operating_balance_transfers_to_entry_fk FOREIGN KEY(to_entry_id) REFERENCES driver_operating_entries(id);
CREATE TRIGGER operating_balance_transfers_append_only
    BEFORE UPDATE OR DELETE ON operating_balance_transfers
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();

CREATE TABLE IF NOT EXISTS no_show_claim_reviews (
    id UUID PRIMARY KEY,
    claim_id UUID NOT NULL REFERENCES driver_no_show_claims(id),
    admin_id UUID NOT NULL REFERENCES admin_users(id),
    outcome TEXT NOT NULL CHECK (outcome IN ('approved','rejected')),
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(claim_id)
);
CREATE TRIGGER no_show_claim_reviews_append_only
    BEFORE UPDATE OR DELETE ON no_show_claim_reviews
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();
