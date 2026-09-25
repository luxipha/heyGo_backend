CREATE TABLE IF NOT EXISTS ride_fares (
    id UUID PRIMARY KEY,
    rider_id UUID NOT NULL REFERENCES users(id),
    package_slug TEXT NOT NULL,
    total_fare_minor NUMERIC(14,2) NOT NULL CHECK (total_fare_minor > 0),
    route JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '15 minutes')
);

CREATE INDEX IF NOT EXISTS ride_fares_rider_id_idx ON ride_fares (rider_id, created_at DESC);

CREATE TABLE IF NOT EXISTS trips (
    id UUID PRIMARY KEY,
    rider_id UUID NOT NULL REFERENCES users(id),
    ride_fare_id UUID NOT NULL REFERENCES ride_fares(id),
    assigned_driver_id UUID REFERENCES users(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'assigned', 'accepted', 'started', 'completed', 'cancelled')),
    cancellation_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    cancelled_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS trips_rider_id_idx ON trips (rider_id, created_at DESC);
CREATE INDEX IF NOT EXISTS trips_driver_id_idx ON trips (assigned_driver_id, created_at DESC);
CREATE INDEX IF NOT EXISTS trips_status_idx ON trips (status, created_at);

CREATE TABLE IF NOT EXISTS drivers (
    id UUID PRIMARY KEY REFERENCES users(id),
    name TEXT NOT NULL,
    profile_pic TEXT NOT NULL DEFAULT '',
    car_plate TEXT NOT NULL,
    package_slug TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('offline', 'available', 'offered', 'on_trip')),
    available BOOLEAN NOT NULL DEFAULT FALSE,
    location GEOGRAPHY(POINT, 4326),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS drivers_match_idx ON drivers (package_slug, status, available);
CREATE INDEX IF NOT EXISTS drivers_location_gist ON drivers USING GIST (location);

CREATE TABLE IF NOT EXISTS driver_assignments (
    trip_id UUID NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    driver_id UUID NOT NULL REFERENCES drivers(id),
    attempt INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('offered', 'accepted', 'declined', 'timed_out', 'cancelled', 'completed')),
    offered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    responded_at TIMESTAMPTZ,
    trip_payload JSONB NOT NULL,
    PRIMARY KEY (trip_id, attempt),
    UNIQUE (trip_id, driver_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS driver_one_active_offer_idx
    ON driver_assignments (driver_id) WHERE status IN ('offered', 'accepted');
CREATE UNIQUE INDEX IF NOT EXISTS trip_one_active_offer_idx
    ON driver_assignments (trip_id) WHERE status IN ('offered', 'accepted');
CREATE INDEX IF NOT EXISTS driver_assignments_expiry_idx
    ON driver_assignments (expires_at) WHERE status = 'offered';

CREATE TABLE IF NOT EXISTS processed_events (
    consumer TEXT NOT NULL,
    event_id TEXT NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (consumer, event_id)
);

CREATE TABLE IF NOT EXISTS trip_ratings (
    trip_id UUID NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    actor_id UUID NOT NULL REFERENCES users(id),
    subject_id UUID NOT NULL REFERENCES users(id),
    rating SMALLINT NOT NULL CHECK (rating BETWEEN 1 AND 5),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (trip_id, actor_id)
);
