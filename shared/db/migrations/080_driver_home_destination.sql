CREATE TABLE driver_home_destinations (
 driver_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 place_id TEXT NOT NULL, name TEXT NOT NULL, address TEXT NOT NULL,
 location GEOGRAPHY(POINT,4326) NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 version BIGINT NOT NULL DEFAULT 1, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Home mode is explicit and owner-controlled. Only the trip's endpoint is
-- filtered; this does not claim a road-route corridor or bypass existing gates.
CREATE FUNCTION driver_home_matches(owner_id UUID,trip_id UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM driver_home_destinations WHERE driver_id=owner_id AND enabled)
 OR EXISTS(SELECT 1 FROM driver_home_destinations h JOIN drivers d ON d.id=h.driver_id
 JOIN trips t ON t.id=trip_id JOIN ride_fares f ON f.id=t.ride_fare_id
 WHERE h.driver_id=owner_id AND h.enabled AND d.location IS NOT NULL AND f.destination IS NOT NULL
 AND ST_Distance(f.destination,h.location)<ST_Distance(d.location,h.location))
$$;
ALTER FUNCTION driver_ride_preference_matches(UUID,UUID) RENAME TO driver_ride_preference_base_matches;
CREATE FUNCTION driver_ride_preference_matches(owner_id UUID,trip_id UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT driver_ride_preference_base_matches(owner_id,trip_id) AND driver_home_matches(owner_id,trip_id)
$$;
