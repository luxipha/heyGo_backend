-- Quote grouping and single-use fare identity remain backend-owned.
ALTER TABLE ride_fares ADD COLUMN preview_id UUID NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE ride_fares ADD COLUMN consumed_trip_id UUID REFERENCES trips(id) ON DELETE SET NULL;
ALTER TABLE ride_fares ADD COLUMN consumed_at TIMESTAMPTZ;
CREATE INDEX ride_fares_preview_idx ON ride_fares(rider_id,preview_id);
-- Guard existing durable terminal vocabulary for Driver compatibility.
ALTER TABLE trips ADD COLUMN matching_deadline TIMESTAMPTZ;
ALTER TABLE trips ADD COLUMN matching_outcome TEXT CHECK(matching_outcome IN ('no_drivers','timeout'));
UPDATE trips SET matching_deadline=created_at+INTERVAL '2 minutes' WHERE status IN ('pending','assigned');
ALTER TABLE trips ALTER COLUMN matching_deadline SET DEFAULT (NOW()+INTERVAL '2 minutes');
CREATE INDEX trips_matching_deadline_idx ON trips(matching_deadline) WHERE status IN ('pending','assigned');
-- Debt approval/reversal and trip creation serialize on the Rider identity.
CREATE FUNCTION lock_rider_debt_identity() RETURNS TRIGGER AS $$
BEGIN
 PERFORM 1 FROM users WHERE id=COALESCE(NEW.rider_id,OLD.rider_id) FOR UPDATE;
 RETURN COALESCE(NEW,OLD);
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER rider_debt_identity_lock BEFORE INSERT OR UPDATE OR DELETE ON rider_no_show_debts
 FOR EACH ROW EXECUTE FUNCTION lock_rider_debt_identity();
ALTER TABLE ride_fares ADD COLUMN pickup_metadata JSONB;
ALTER TABLE ride_fares ADD COLUMN destination_metadata JSONB;
