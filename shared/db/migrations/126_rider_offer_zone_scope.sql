-- Optional zone scope for rider offers. An empty array keeps the existing
-- market-wide behaviour, so no current offer changes. A non-empty array limits
-- the offer to fares whose pickup lies inside one of the referenced approved
-- region/airport geofences (checked when quoting and again when reserving).
ALTER TABLE rider_offers
 ADD COLUMN IF NOT EXISTS zone_ids UUID[] NOT NULL DEFAULT '{}',
 ADD CONSTRAINT rider_offers_zone_ids_limit CHECK(cardinality(zone_ids)<=20);
