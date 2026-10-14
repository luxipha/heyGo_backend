CREATE TABLE IF NOT EXISTS refund_policy_retirements (
    id UUID PRIMARY KEY,
    policy_id UUID NOT NULL REFERENCES refund_policy_versions(id),
    effective_until TIMESTAMPTZ NOT NULL,
    requested_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    status TEXT NOT NULL CHECK (status IN ('pending','approved')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at TIMESTAMPTZ,
    CHECK ((approved_by IS NULL) = (approved_at IS NULL)),
    CHECK ((status='approved') = (approved_by IS NOT NULL AND approved_at IS NOT NULL)),
    CHECK (approved_by IS NULL OR approved_by<>requested_by)
);
CREATE UNIQUE INDEX IF NOT EXISTS refund_policy_pending_retirement_idx
    ON refund_policy_retirements(policy_id) WHERE status='pending';
