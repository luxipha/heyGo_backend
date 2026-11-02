ALTER TABLE pricing_versions ADD COLUMN zone_revision BIGINT NOT NULL DEFAULT 0;
CREATE TABLE pricing_zone_rules (
 pricing_id UUID NOT NULL REFERENCES pricing_versions(id),
 zone_id UUID NOT NULL REFERENCES trip_geofences(id),
 zone_name TEXT NOT NULL,
 boundary geometry(Geometry,4326) NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('multiplier','fixed')),
 value BIGINT NOT NULL,
 PRIMARY KEY(pricing_id,zone_id),
 CHECK((kind='multiplier' AND value BETWEEN 1000 AND 50000) OR (kind='fixed' AND value BETWEEN 0 AND 1000000))
);
