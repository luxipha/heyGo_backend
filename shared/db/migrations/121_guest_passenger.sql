-- Guest identity belongs to the booking; the authenticated rider remains the booker.
ALTER TABLE ride_fares ADD COLUMN guest_name TEXT NOT NULL DEFAULT '', ADD COLUMN guest_phone TEXT NOT NULL DEFAULT '',
 ADD CONSTRAINT fare_guest_valid CHECK ((guest_name='' AND guest_phone='') OR (char_length(guest_name) BETWEEN 1 AND 100 AND guest_phone ~ '^\+[1-9][0-9]{6,14}$'));
ALTER TABLE trips ADD COLUMN guest_name TEXT NOT NULL DEFAULT '', ADD COLUMN guest_phone TEXT NOT NULL DEFAULT '',
 ADD CONSTRAINT trip_guest_valid CHECK ((guest_name='' AND guest_phone='') OR (char_length(guest_name) BETWEEN 1 AND 100 AND guest_phone ~ '^\+[1-9][0-9]{6,14}$'));
CREATE FUNCTION copy_trip_guest_passenger() RETURNS TRIGGER AS $$
BEGIN
 SELECT guest_name,guest_phone INTO NEW.guest_name,NEW.guest_phone FROM ride_fares WHERE id=NEW.ride_fare_id;
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trip_guest_passenger BEFORE INSERT ON trips FOR EACH ROW EXECUTE FUNCTION copy_trip_guest_passenger();
