CREATE TABLE IF NOT EXISTS casperid_trust_deliveries (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    external_event_id TEXT NOT NULL UNIQUE,
    casper_id_user_id TEXT NOT NULL,
    actor_role TEXT NOT NULL CHECK (actor_role IN ('consumer','provider')),
    event_type TEXT NOT NULL,
    interaction_reference TEXT NOT NULL,
    rating INTEGER CHECK (rating IS NULL OR rating BETWEEN 1 AND 5),
    occurred_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT NOT NULL DEFAULT '',
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((status='delivered') = (delivered_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS casperid_trust_deliveries_pending_idx
    ON casperid_trust_deliveries(next_attempt_at,id) WHERE status='pending';
