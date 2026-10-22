CREATE TABLE driver_notification_preferences (
 driver_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 ride_requests BOOLEAN NOT NULL DEFAULT TRUE,
 trip_updates BOOLEAN NOT NULL DEFAULT TRUE,
 earnings BOOLEAN NOT NULL DEFAULT TRUE,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE FUNCTION driver_notification_allowed(owner_id UUID, category TEXT) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE((SELECT CASE category WHEN 'dispatch' THEN p.ride_requests WHEN 'trip' THEN p.trip_updates WHEN 'earnings' THEN p.earnings WHEN 'balance' THEN p.earnings ELSE TRUE END FROM driver_notification_preferences p WHERE p.driver_id=owner_id),TRUE)
$$;
UPDATE driver_notifications SET type='dispatch' WHERE type='trip' AND title='New ride request';
UPDATE driver_notifications SET type='earnings' WHERE type='trip' AND title IN ('Trip payment updated','No-show claim updated');
