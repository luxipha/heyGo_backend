ALTER TABLE scheduled_ride_policies
 ADD COLUMN free_cancellation_minutes INT NOT NULL DEFAULT 60 CHECK(free_cancellation_minutes BETWEEN 10 AND 10080),
 ADD COLUMN pickup_window_minutes INT NOT NULL DEFAULT 5 CHECK(pickup_window_minutes BETWEEN 0 AND 30),
 ADD COLUMN rider_reminder_minutes INT NOT NULL DEFAULT 60 CHECK(rider_reminder_minutes BETWEEN 5 AND 1440);

ALTER TABLE scheduled_rides
 ADD COLUMN free_cancellation_until TIMESTAMPTZ,
 ADD COLUMN pickup_window_end TIMESTAMPTZ,
 ADD COLUMN payment_method TEXT NOT NULL DEFAULT 'direct_to_driver' CHECK(payment_method='direct_to_driver'),
 ADD COLUMN cancellation_reason_code TEXT,
 ADD COLUMN cancellation_reason_note TEXT NOT NULL DEFAULT '',
 ADD COLUMN rider_reminder_enabled BOOLEAN NOT NULL DEFAULT false,
 ADD COLUMN rider_reminder_at TIMESTAMPTZ,
 ADD COLUMN rider_reminder_sent_at TIMESTAMPTZ;

-- Keep the previously advertised entitlement for existing bookings.
-- Only new bookings use the new one-hour cancellation policy.
UPDATE scheduled_rides SET free_cancellation_until=dispatch_at,
 pickup_window_end=pickup_at,rider_reminder_at=pickup_at-INTERVAL '1 hour';

ALTER TABLE scheduled_rides ALTER COLUMN free_cancellation_until SET NOT NULL;
ALTER TABLE scheduled_rides ALTER COLUMN pickup_window_end SET NOT NULL;

CREATE INDEX scheduled_rider_reminders_due ON scheduled_rides(rider_reminder_at)
 WHERE rider_reminder_enabled AND rider_reminder_sent_at IS NULL;
