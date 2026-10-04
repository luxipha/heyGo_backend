-- Refunds remain disabled until an approved policy and explicit provider
-- readiness flag. Every in-flight refund reserves balance across all debits.
ALTER TABLE driver_operating_accounts ADD COLUMN IF NOT EXISTS refund_reserved_kobo BIGINT NOT NULL DEFAULT 0
    CHECK (refund_reserved_kobo>=0);
CREATE OR REPLACE FUNCTION protect_refund_reservation() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.refund_reserved_kobo>0 AND NEW.balance_kobo<NEW.refund_reserved_kobo THEN
        RAISE EXCEPTION 'operating balance is reserved for a pending refund';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER driver_operating_refund_reservation BEFORE INSERT OR UPDATE ON driver_operating_accounts
    FOR EACH ROW EXECUTE FUNCTION protect_refund_reservation();

CREATE TABLE IF NOT EXISTS refund_policy_versions (
    id UUID PRIMARY KEY,
    version INTEGER NOT NULL UNIQUE CHECK (version>0),
    reason_codes TEXT[] NOT NULL CHECK (cardinality(reason_codes)>0),
    max_request_kobo BIGINT NOT NULL CHECK (max_request_kobo>=10000),
    max_daily_kobo BIGINT NOT NULL CHECK (max_daily_kobo>=max_request_kobo),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_to TIMESTAMPTZ,
    state TEXT NOT NULL DEFAULT 'draft' CHECK (state IN ('draft','approved')),
    proposed_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at TIMESTAMPTZ,
    CHECK (effective_to IS NULL OR effective_to>effective_from),
    CHECK ((state='approved')=(approved_by IS NOT NULL AND approved_at IS NOT NULL)),
    CHECK (approved_by IS NULL OR approved_by<>proposed_by)
);

CREATE TABLE IF NOT EXISTS refund_requests (
    id UUID PRIMARY KEY,
    topup_id UUID NOT NULL REFERENCES driver_operating_topups(id),
    driver_id UUID NOT NULL REFERENCES users(id),
    case_id UUID NOT NULL REFERENCES support_cases(id),
    policy_id UUID NOT NULL REFERENCES refund_policy_versions(id),
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo>=10000),
    reason_code TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (char_length(trim(reason)) BETWEEN 5 AND 1000),
    state TEXT NOT NULL DEFAULT 'draft' CHECK (state IN ('draft','approved','provider_pending','needs_reconciliation','completed','failed')),
    proposed_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    refund_reference TEXT NOT NULL UNIQUE,
    provider_status TEXT,
    provider_transaction_reference TEXT,
    ledger_entry_id UUID REFERENCES driver_operating_entries(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    CHECK (approved_by IS NULL OR approved_by<>proposed_by)
);
CREATE UNIQUE INDEX IF NOT EXISTS refund_requests_active_topup_idx ON refund_requests(topup_id)
    WHERE state<>'failed';
CREATE INDEX IF NOT EXISTS refund_requests_queue_idx ON refund_requests(state,updated_at,id);

CREATE TABLE IF NOT EXISTS refund_destinations (
    refund_id UUID PRIMARY KEY REFERENCES refund_requests(id),
    bank_code TEXT NOT NULL,
    account_number_ciphertext BYTEA NOT NULL,
    account_number_masked TEXT NOT NULL,
    account_name_ciphertext BYTEA NOT NULL,
    name_matched BOOLEAN NOT NULL,
    name_record_id UUID REFERENCES driver_verified_legal_names(id),
    verified_at TIMESTAMPTZ NOT NULL,
    supplied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS refund_actions (
    id UUID PRIMARY KEY,
    refund_id UUID NOT NULL REFERENCES refund_requests(id),
    actor_admin_id UUID REFERENCES admin_users(id),
    action TEXT NOT NULL,
    reason TEXT NOT NULL,
    before_state TEXT,
    after_state TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS refund_actions_refund_idx ON refund_actions(refund_id,created_at,id);
CREATE TRIGGER refund_actions_append_only BEFORE UPDATE OR DELETE ON refund_actions
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();
