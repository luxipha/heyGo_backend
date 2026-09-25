-- Keep the rider-requested endpoints separate from the routing provider's
-- snapped geometry. Legacy fare rows remain nullable until they expire.
ALTER TABLE ride_fares ADD COLUMN IF NOT EXISTS pickup GEOGRAPHY(POINT, 4326);
ALTER TABLE ride_fares ADD COLUMN IF NOT EXISTS destination GEOGRAPHY(POINT, 4326);
