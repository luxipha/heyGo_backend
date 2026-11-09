ALTER TABLE ride_fares ADD COLUMN pickup_note TEXT NOT NULL DEFAULT '' CHECK(char_length(pickup_note)<=500);
ALTER TABLE trips ADD COLUMN pickup_note TEXT NOT NULL DEFAULT '' CHECK(char_length(pickup_note)<=500);
ALTER TABLE trips ADD COLUMN stops JSONB NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(stops)='array' AND jsonb_array_length(stops)<=3);
ALTER TABLE trips ADD COLUMN route_revision BIGINT NOT NULL DEFAULT 0;

CREATE FUNCTION copy_trip_pickup_note() RETURNS TRIGGER AS $$
BEGIN
 SELECT pickup_note INTO NEW.pickup_note FROM ride_fares WHERE id=NEW.ride_fare_id;
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trip_pickup_note BEFORE INSERT ON trips FOR EACH ROW EXECUTE FUNCTION copy_trip_pickup_note();

CREATE TABLE trip_amendment_quotes (
 id UUID PRIMARY KEY,
 trip_id UUID NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
 rider_id UUID NOT NULL REFERENCES users(id),
 trip_version BIGINT NOT NULL,
 trip_status TEXT NOT NULL,
 old_fare_id UUID NOT NULL REFERENCES ride_fares(id),
 destination JSONB NOT NULL,
 stops JSONB NOT NULL,
 route JSONB NOT NULL,
 origin GEOGRAPHY(POINT,4326) NOT NULL,
 fare_kobo BIGINT NOT NULL CHECK(fare_kobo>0 AND fare_kobo<=10000000),
 previous_fare_kobo BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW()+INTERVAL '60 seconds',
 applied_at TIMESTAMPTZ,
 applied_version BIGINT,
 replacement_fare_id UUID UNIQUE REFERENCES ride_fares(id)
);
CREATE INDEX trip_amendment_owner_idx ON trip_amendment_quotes(rider_id,trip_id,created_at DESC);

CREATE TABLE trip_safety_monitor (
 trip_id UUID PRIMARY KEY REFERENCES trips(id) ON DELETE CASCADE,
 route_revision BIGINT NOT NULL,
 anchor GEOGRAPHY(POINT,4326) NOT NULL,
 stationary_since TIMESTAMPTZ NOT NULL,
 last_at TIMESTAMPTZ NOT NULL,
 deviation_since TIMESTAMPTZ,
 deviation_samples INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE trip_safety_checkins (
 id UUID PRIMARY KEY,
 trip_id UUID NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
 rider_id UUID NOT NULL REFERENCES users(id),
 kind TEXT NOT NULL CHECK(kind IN ('ride_stopped','route_deviation')),
 route_revision BIGINT NOT NULL,
 observed_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 response TEXT CHECK(response IN ('okay','need_help')),
 responded_at TIMESTAMPTZ,
 UNIQUE(trip_id,kind,route_revision),
 CHECK((response IS NULL)=(responded_at IS NULL))
);
CREATE INDEX trip_checkins_owner_idx ON trip_safety_checkins(rider_id,trip_id,created_at DESC);
