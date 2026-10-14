CREATE TABLE rider_trip_commands (
 id UUID PRIMARY KEY,
 rider_id UUID NOT NULL REFERENCES users(id),
 trip_id UUID NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
 command TEXT NOT NULL CHECK(command IN ('trip.cmd.cancel','trip.cmd.rate')),
 payload JSONB NOT NULL,
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','confirmed','rejected')),
 reason TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 resolved_at TIMESTAMPTZ
);
CREATE INDEX rider_trip_commands_owner_idx ON rider_trip_commands(rider_id,trip_id,created_at DESC);
