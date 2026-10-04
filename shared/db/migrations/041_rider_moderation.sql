-- A restriction blocks only new Trip creation. Review dates do not silently
-- restore access; Safety must record an explicit, audited lift.
ALTER TABLE users ADD COLUMN IF NOT EXISTS rider_trip_restricted BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS rider_restrictions (
    id UUID PRIMARY KEY,
    rider_id UUID NOT NULL REFERENCES users(id),
    case_id UUID NOT NULL REFERENCES support_cases(id),
    status TEXT NOT NULL CHECK (status IN ('active','lifted')),
    reason TEXT NOT NULL CHECK (char_length(trim(reason)) BETWEEN 5 AND 1000),
    review_at TIMESTAMPTZ NOT NULL,
    applied_by UUID NOT NULL REFERENCES admin_users(id),
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lifted_by UUID REFERENCES admin_users(id),
    lifted_at TIMESTAMPTZ,
    lift_reason TEXT,
    CHECK ((status='lifted')=(lifted_by IS NOT NULL AND lifted_at IS NOT NULL AND lift_reason IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS rider_one_active_restriction_idx ON rider_restrictions(rider_id) WHERE status='active';
CREATE INDEX IF NOT EXISTS rider_restrictions_history_idx ON rider_restrictions(rider_id,applied_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS rider_restriction_actions (
    id UUID PRIMARY KEY,
    restriction_id UUID NOT NULL REFERENCES rider_restrictions(id),
    actor_admin_id UUID NOT NULL REFERENCES admin_users(id),
    action TEXT NOT NULL CHECK (action IN ('applied','lifted')),
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS rider_restriction_actions_idx ON rider_restriction_actions(restriction_id,created_at,id);
CREATE TRIGGER rider_restriction_actions_append_only BEFORE UPDATE OR DELETE ON rider_restriction_actions
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();

CREATE OR REPLACE FUNCTION reject_restricted_rider_trip() RETURNS TRIGGER AS $$
DECLARE restricted BOOLEAN;
BEGIN
    -- Restriction changes UPDATE this user row. Under Repeatable Read, a
    -- concurrent update causes a serialization retry rather than a stale pass.
    SELECT rider_trip_restricted INTO restricted FROM users WHERE id=NEW.rider_id FOR UPDATE;
    IF restricted THEN
        RAISE EXCEPTION 'rider_trip_restricted' USING ERRCODE='P0001';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trips_reject_restricted_rider BEFORE INSERT ON trips
    FOR EACH ROW EXECUTE FUNCTION reject_restricted_rider_trip();

CREATE TABLE IF NOT EXISTS rider_flags (
    id UUID PRIMARY KEY,
    rider_id UUID NOT NULL REFERENCES users(id),
    kind TEXT NOT NULL CHECK (kind IN ('support','payment','safety')),
    case_id UUID NOT NULL REFERENCES support_cases(id),
    incident_id UUID REFERENCES safety_incidents(id),
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
    reason TEXT NOT NULL CHECK (char_length(trim(reason)) BETWEEN 5 AND 1000),
    review_at TIMESTAMPTZ NOT NULL,
    created_by UUID NOT NULL REFERENCES admin_users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_by UUID REFERENCES admin_users(id),
    resolved_at TIMESTAMPTZ,
    resolution_reason TEXT,
    CHECK ((kind='safety')=(incident_id IS NOT NULL)),
    CHECK ((status='resolved')=(resolved_by IS NOT NULL AND resolved_at IS NOT NULL AND resolution_reason IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS rider_flags_history_idx ON rider_flags(rider_id,created_at DESC,id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS rider_flag_one_open_source_idx ON rider_flags(rider_id,kind,case_id) WHERE status='open';

CREATE TABLE IF NOT EXISTS rider_flag_actions (
    id UUID PRIMARY KEY,
    flag_id UUID NOT NULL REFERENCES rider_flags(id),
    actor_admin_id UUID NOT NULL REFERENCES admin_users(id),
    action TEXT NOT NULL CHECK (action IN ('created','resolved')),
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS rider_flag_actions_idx ON rider_flag_actions(flag_id,created_at,id);
CREATE TRIGGER rider_flag_actions_append_only BEFORE UPDATE OR DELETE ON rider_flag_actions
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();
