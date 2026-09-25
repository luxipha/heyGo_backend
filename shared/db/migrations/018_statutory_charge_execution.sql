-- Charge geography and unpaid assessed amounts are persisted explicitly.
-- Statutory rules remain inactive until a second staff account approves them.
ALTER TABLE statutory_charge_rules
    ADD COLUMN IF NOT EXISTS region_match TEXT NOT NULL DEFAULT 'either'
    CHECK (region_match IN ('pickup','destination','either','both'));

CREATE TABLE IF NOT EXISTS statutory_charge_rule_retirements (
    id UUID PRIMARY KEY,
    rule_id UUID NOT NULL REFERENCES statutory_charge_rules(id),
    effective_until TIMESTAMPTZ NOT NULL,
    requested_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    status TEXT NOT NULL CHECK (status IN ('pending','approved')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at TIMESTAMPTZ,
    CHECK ((approved_by IS NULL)=(approved_at IS NULL)),
    CHECK (approved_by IS NULL OR approved_by<>requested_by)
);
CREATE UNIQUE INDEX IF NOT EXISTS statutory_rule_pending_retirement_idx
    ON statutory_charge_rule_retirements(rule_id) WHERE status='pending';

-- This append-only ledger tracks repayment against immutable assessed charges.
CREATE TABLE IF NOT EXISTS statutory_charge_repayments (
    id UUID PRIMARY KEY,
    charge_id UUID NOT NULL REFERENCES trip_statutory_charges(id),
    driver_id UUID NOT NULL REFERENCES users(id),
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo > 0),
    operating_entry_id UUID NOT NULL UNIQUE REFERENCES driver_operating_entries(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS statutory_charge_repayments_driver_idx
    ON statutory_charge_repayments(driver_id, created_at DESC);

CREATE TRIGGER statutory_charge_repayments_append_only
    BEFORE UPDATE OR DELETE ON statutory_charge_repayments
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();

-- Allow linking the debit created in the same trip transaction, exactly once;
-- all assessment content remains immutable.
DROP TRIGGER IF EXISTS trip_statutory_charges_append_only ON trip_statutory_charges;
CREATE OR REPLACE FUNCTION protect_trip_charge_snapshot() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP='DELETE' OR (to_jsonb(NEW)-'operating_entry_id') IS DISTINCT FROM (to_jsonb(OLD)-'operating_entry_id')
       OR OLD.operating_entry_id IS NOT NULL OR NEW.operating_entry_id IS NULL THEN
        RAISE EXCEPTION 'statutory charge snapshots are append only';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trip_statutory_charges_append_only
    BEFORE UPDATE OR DELETE ON trip_statutory_charges
    FOR EACH ROW EXECUTE FUNCTION protect_trip_charge_snapshot();

CREATE OR REPLACE FUNCTION driver_balance_eligible(target_driver UUID, target_market TEXT) RETURNS BOOLEAN AS $$
    SELECT target_market IS NOT NULL AND COUNT(*) = 1
       AND COALESCE((SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=target_driver),0) >= MIN(p.minimum_kobo)
       AND COALESCE((SELECT SUM(c.amount_kobo-COALESCE(r.repaid_kobo,0))
                     FROM trip_statutory_charges c LEFT JOIN
                       (SELECT charge_id,SUM(amount_kobo) AS repaid_kobo FROM statutory_charge_repayments GROUP BY charge_id) r
                       ON r.charge_id=c.id WHERE c.driver_id=target_driver
                         AND c.bearer='driver' AND c.funding_source='operating_balance'),0)=0
    FROM operating_balance_policies p
    WHERE p.market_code=target_market AND p.status='approved'
      AND p.effective_from<=NOW() AND (p.effective_until IS NULL OR p.effective_until>NOW())
$$ LANGUAGE sql STABLE;
