CREATE TABLE driver_ride_preferences (
 driver_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 ride_types TEXT[] NOT NULL DEFAULT '{}',
 long_distance BOOLEAN NOT NULL DEFAULT TRUE,
 airport BOOLEAN NOT NULL DEFAULT TRUE,
 auto_accept BOOLEAN NOT NULL DEFAULT FALSE,
 working_areas TEXT[] NOT NULL DEFAULT '{}',
 version BIGINT NOT NULL DEFAULT 1,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- No preference row means all otherwise eligible offers; never bypass eligibility.
CREATE FUNCTION driver_ride_preference_matches(owner_id UUID, trip_id UUID) RETURNS BOOLEAN
LANGUAGE SQL STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM driver_ride_preferences WHERE driver_id=owner_id) OR EXISTS(SELECT 1
 FROM driver_ride_preferences p JOIN trips t ON t.id=trip_id JOIN ride_fares f ON f.id=t.ride_fare_id
 WHERE p.driver_id=owner_id AND (cardinality(p.ride_types)=0 OR f.package_slug=ANY(p.ride_types))
  AND (p.long_distance OR COALESCE((f.route->'routes'->0->>'distance')::NUMERIC,(f.route->>'distanceMeters')::NUMERIC,1e20)<=25000)
  AND (p.airport OR NOT (t.regulatory_tags && ARRAY['AIRPORT_PICKUP','AIRPORT_DROPOFF']))
  AND (cardinality(p.working_areas)=0 OR t.origin_region_code=ANY(p.working_areas))
)
$$;
