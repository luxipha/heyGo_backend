-- Accrual is based on completed trips, independent of rider payment and payout.
-- Snapshot the commission on each trip so later policy changes cannot rewrite
-- historical earnings. The current HeyGo commission is 0 basis points.
CREATE TABLE IF NOT EXISTS driver_trip_earnings (
    trip_id UUID PRIMARY KEY REFERENCES trips(id) ON DELETE CASCADE,
    driver_id UUID NOT NULL REFERENCES users(id),
    fare_kobo BIGINT NOT NULL CHECK (fare_kobo >= 0),
    commission_bps INTEGER NOT NULL CHECK (commission_bps BETWEEN 0 AND 10000),
    commission_kobo BIGINT NOT NULL CHECK (commission_kobo >= 0),
    earnings_kobo BIGINT NOT NULL CHECK (earnings_kobo = fare_kobo - commission_kobo AND earnings_kobo >= 0),
    accrued_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS driver_trip_earnings_driver_day_idx ON driver_trip_earnings(driver_id, accrued_at DESC);

-- Preserve earnings for trips completed before this table was introduced.
INSERT INTO driver_trip_earnings(trip_id,driver_id,fare_kobo,commission_bps,commission_kobo,earnings_kobo,accrued_at)
SELECT t.id,t.assigned_driver_id,ROUND(f.total_fare_minor)::BIGINT,0,0,ROUND(f.total_fare_minor)::BIGINT,t.completed_at
FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id
WHERE t.status='completed' AND t.assigned_driver_id IS NOT NULL AND t.completed_at IS NOT NULL
ON CONFLICT(trip_id) DO NOTHING;
