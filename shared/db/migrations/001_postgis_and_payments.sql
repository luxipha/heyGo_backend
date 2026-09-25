CREATE EXTENSION IF NOT EXISTS postgis;

CREATE TABLE IF NOT EXISTS payments (
    id UUID PRIMARY KEY,
    trip_id TEXT NOT NULL UNIQUE,
    rider_id TEXT NOT NULL,
    driver_id TEXT NOT NULL,
    amount BIGINT NOT NULL CHECK (amount > 0),
    currency CHAR(3) NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'success', 'failed', 'cancelled')),
    payment_reference TEXT NOT NULL UNIQUE,
    transaction_reference TEXT NOT NULL UNIQUE,
    checkout_url TEXT NOT NULL,
    provider_metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS payment_webhook_events (
    event_id TEXT PRIMARY KEY,
    published BOOLEAN NOT NULL DEFAULT FALSE,
    processed_at TIMESTAMPTZ NOT NULL
);

-- The driver repository will use this geography column for indexed, meter-accurate proximity queries.
CREATE TABLE IF NOT EXISTS driver_locations (
    driver_id TEXT PRIMARY KEY,
    location GEOGRAPHY(POINT, 4326) NOT NULL,
    available BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS driver_locations_location_gist
    ON driver_locations USING GIST (location);
