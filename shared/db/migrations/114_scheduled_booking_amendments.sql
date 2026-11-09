-- Immutable receipt of a Rider-confirmed replacement quote. No existing rows
-- or monetary ledgers are modified by this migration.
CREATE TABLE scheduled_ride_amendments (
 booking_id UUID NOT NULL REFERENCES scheduled_rides(id), request_id UUID NOT NULL,
 old_fare_id UUID NOT NULL REFERENCES ride_fares(id), new_fare_id UUID NOT NULL REFERENCES ride_fares(id),
 pickup_at TIMESTAMPTZ NOT NULL, previous_driver_id UUID REFERENCES users(id),
 resulting_version BIGINT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(booking_id,request_id)
);
CREATE TRIGGER scheduled_amendment_immutable BEFORE UPDATE OR DELETE ON scheduled_ride_amendments FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();

-- Separate receipts preserve the existing offer/decline history and keys.
CREATE TABLE scheduled_ride_amendment_notifications (
 booking_id UUID NOT NULL,
 request_id UUID NOT NULL,
 driver_id UUID NOT NULL REFERENCES users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(booking_id,request_id,driver_id),
 FOREIGN KEY(booking_id,request_id) REFERENCES scheduled_ride_amendments(booking_id,request_id)
);
CREATE TABLE scheduled_ride_amendment_declines (
 booking_id UUID NOT NULL,
 request_id UUID NOT NULL,
 driver_id UUID NOT NULL REFERENCES users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(booking_id,request_id,driver_id),
 FOREIGN KEY(booking_id,request_id) REFERENCES scheduled_ride_amendments(booking_id,request_id)
);
