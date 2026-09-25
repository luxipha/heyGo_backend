-- Arrival is a durable step before the driver's explicit Start Trip action.
ALTER TABLE trips DROP CONSTRAINT IF EXISTS trips_status_check;
ALTER TABLE trips ADD CONSTRAINT trips_status_check
    CHECK (status IN ('pending','assigned','accepted','arrived','started','completed','cancelled'));
ALTER TABLE trips ADD COLUMN IF NOT EXISTS arrived_at TIMESTAMPTZ;

-- Direct rider-to-driver settlement is recorded as a driver assertion. It does
-- not represent bank/provider verification or a HeyGo collection/payout.
CREATE TABLE IF NOT EXISTS trip_settlements (
    trip_id UUID PRIMARY KEY REFERENCES trips(id),
    driver_id UUID NOT NULL REFERENCES users(id),
    rider_id UUID NOT NULL REFERENCES users(id),
    expected_amount_kobo BIGINT NOT NULL CHECK (expected_amount_kobo > 0),
    status TEXT NOT NULL CHECK (status IN ('pending','confirmed','disputed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ,
    CHECK ((status='pending')=(resolved_at IS NULL))
);
CREATE INDEX IF NOT EXISTS trip_settlements_driver_idx
    ON trip_settlements(driver_id, created_at DESC);

CREATE TABLE IF NOT EXISTS trip_settlement_actions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    trip_id UUID NOT NULL REFERENCES trip_settlements(trip_id),
    actor_id UUID NOT NULL REFERENCES users(id),
    action TEXT NOT NULL CHECK (action IN ('pending','confirmed','disputed')),
    note TEXT,
    idempotency_key TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS trip_settlement_actions_trip_idx
    ON trip_settlement_actions(trip_id, created_at);

CREATE TRIGGER trip_settlement_actions_append_only
    BEFORE UPDATE OR DELETE ON trip_settlement_actions
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();
