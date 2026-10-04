-- Capture future Trip transitions. Existing Trips are intentionally not
-- backfilled: current status and milestone columns cannot reconstruct every
-- historical transition or its exact time.
CREATE TABLE IF NOT EXISTS trip_status_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    trip_id UUID NOT NULL,
    previous_status TEXT,
    status TEXT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    source TEXT NOT NULL DEFAULT 'database_trigger'
);
CREATE INDEX IF NOT EXISTS trip_status_events_trip_idx ON trip_status_events(trip_id,id);

CREATE OR REPLACE FUNCTION capture_trip_status_event() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP='INSERT' OR NEW.status IS DISTINCT FROM OLD.status THEN
        INSERT INTO trip_status_events(trip_id,previous_status,status)
        VALUES(NEW.id,CASE WHEN TG_OP='INSERT' THEN NULL ELSE OLD.status END,NEW.status);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trips_capture_status_event AFTER INSERT OR UPDATE OF status ON trips
    FOR EACH ROW EXECUTE FUNCTION capture_trip_status_event();

-- No foreign key is used here: a permitted Trip deletion must not silently
-- rewrite this evidence. The record contains only a Trip UUID and statuses;
-- deployed retention policy still needs a separate decision.
CREATE OR REPLACE FUNCTION protect_trip_status_event() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'trip status events are append only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trip_status_events_append_only BEFORE UPDATE OR DELETE ON trip_status_events
    FOR EACH ROW EXECUTE FUNCTION protect_trip_status_event();

ALTER TABLE trips ADD COLUMN IF NOT EXISTS accepted_vehicle_snapshot JSONB;
ALTER TABLE trips ADD COLUMN IF NOT EXISTS accepted_vehicle_snapshot_at TIMESTAMPTZ;

CREATE OR REPLACE FUNCTION capture_trip_vehicle_snapshot() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.accepted_vehicle_snapshot IS DISTINCT FROM OLD.accepted_vehicle_snapshot THEN
        RAISE EXCEPTION 'accepted vehicle snapshot is immutable';
    END IF;
    IF NEW.accepted_vehicle_snapshot_at IS DISTINCT FROM OLD.accepted_vehicle_snapshot_at THEN
        RAISE EXCEPTION 'accepted vehicle snapshot time is immutable';
    END IF;
    IF NEW.status='accepted' AND OLD.status IS DISTINCT FROM NEW.status
       AND OLD.accepted_vehicle_snapshot IS NULL AND NEW.assigned_driver_id IS NOT NULL THEN
        SELECT jsonb_build_object(
            'driverId',d.id,'packageSlug',COALESCE(NULLIF(v.package_slug,''),d.package_slug),
            'make',v.make,'model',v.model,'modelYear',v.model_year,'color',v.color,
            'plate',COALESCE(NULLIF(v.plate,''),d.car_plate))
        INTO NEW.accepted_vehicle_snapshot
        FROM drivers d LEFT JOIN driver_vehicles v ON v.driver_id=d.id
        WHERE d.id=NEW.assigned_driver_id;
        IF NEW.accepted_vehicle_snapshot IS NOT NULL THEN
            NEW.accepted_vehicle_snapshot_at := NOW();
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trips_capture_vehicle_snapshot BEFORE UPDATE ON trips
    FOR EACH ROW EXECUTE FUNCTION capture_trip_vehicle_snapshot();
