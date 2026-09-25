-- Socket GPS is retained even before a driver requests to go online.
CREATE TABLE IF NOT EXISTS driver_live_locations (
    driver_id UUID PRIMARY KEY REFERENCES users(id),
    location GEOGRAPHY(POINT,4326) NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS online_market_code TEXT;

-- A missing or overlapping active market resolves to NULL and fails closed.
CREATE OR REPLACE FUNCTION driver_market_at(point GEOGRAPHY) RETURNS TEXT AS $$
    SELECT CASE WHEN COUNT(*) = 1 THEN MIN(code) ELSE NULL END
    FROM trip_geofences
    WHERE kind='market' AND status='approved'
      AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())
      AND ST_Covers(boundary, point::geometry)
$$ LANGUAGE sql STABLE;

CREATE OR REPLACE FUNCTION driver_balance_eligible(target_driver UUID, target_market TEXT) RETURNS BOOLEAN AS $$
    SELECT target_market IS NOT NULL AND COUNT(*) = 1
       AND COALESCE((SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=target_driver),0) >= MIN(minimum_kobo)
    FROM operating_balance_policies
    WHERE market_code=target_market AND status='approved'
      AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())
$$ LANGUAGE sql STABLE;

CREATE OR REPLACE FUNCTION refresh_driver_operating_market(target_driver UUID) RETURNS VOID AS $$
    UPDATE drivers d SET location=l.location,online_market_code=driver_market_at(l.location),
        status=CASE WHEN d.status IN ('on_trip','offered') THEN d.status
            WHEN d.online_requested AND l.recorded_at>=NOW()-INTERVAL '30 minutes'
             AND driver_balance_eligible(d.id,driver_market_at(l.location))
             AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=d.id),FALSE)
            THEN 'available' ELSE 'offline' END,
        available=CASE WHEN d.status IN ('on_trip','offered') THEN FALSE
            ELSE d.online_requested AND l.recorded_at>=NOW()-INTERVAL '30 minutes'
             AND driver_balance_eligible(d.id,driver_market_at(l.location))
             AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=d.id),FALSE) END,
        last_seen_at=NOW(),updated_at=NOW()
    FROM driver_live_locations l WHERE d.id=target_driver AND l.driver_id=d.id;
$$ LANGUAGE sql VOLATILE;
