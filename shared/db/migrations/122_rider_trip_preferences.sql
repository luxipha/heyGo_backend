ALTER TABLE ride_fares ADD COLUMN ride_preferences JSONB NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(ride_preferences)='object');
ALTER TABLE trips ADD COLUMN ride_preferences JSONB NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(ride_preferences)='object'), ADD COLUMN preferences_revision BIGINT NOT NULL DEFAULT 0;
CREATE FUNCTION copy_trip_ride_preferences() RETURNS TRIGGER AS $$
BEGIN
 SELECT ride_preferences INTO NEW.ride_preferences FROM ride_fares WHERE id=NEW.ride_fare_id;
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trip_ride_preferences BEFORE INSERT ON trips FOR EACH ROW EXECUTE FUNCTION copy_trip_ride_preferences();
