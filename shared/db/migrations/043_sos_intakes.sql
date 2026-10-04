-- Coordinates are deliberately separate from the Support case and ordinary
-- incident reads. A resolved incident starts a 30-day location retention clock.
CREATE TABLE IF NOT EXISTS sos_intakes (
    id UUID PRIMARY KEY,
    incident_id UUID NOT NULL UNIQUE REFERENCES safety_incidents(id),
    trip_id UUID NOT NULL REFERENCES trips(id),
    reporter_id UUID NOT NULL REFERENCES users(id),
    client_request_id UUID NOT NULL,
    location GEOGRAPHY(POINT,4326),
    location_source TEXT NOT NULL CHECK (location_source IN ('reporter','driver_fallback','unavailable')),
    location_recorded_at TIMESTAMPTZ,
    location_purge_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(reporter_id,client_request_id),
    CHECK ((location IS NULL)=(location_recorded_at IS NULL)),
    CHECK (location IS NOT NULL OR location_source='unavailable')
);
CREATE INDEX IF NOT EXISTS sos_intakes_purge_idx ON sos_intakes(location_purge_at)
    WHERE location IS NOT NULL;

CREATE OR REPLACE FUNCTION set_sos_location_purge_at() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status='resolved' AND NEW.resolved_at IS NOT NULL THEN
        UPDATE sos_intakes SET location_purge_at=NEW.resolved_at+INTERVAL '30 days'
        WHERE incident_id=NEW.id;
    ELSIF OLD.status='resolved' AND NEW.status<>'resolved' THEN
        UPDATE sos_intakes SET location_purge_at=NULL WHERE incident_id=NEW.id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER safety_incident_sos_retention AFTER UPDATE OF status,resolved_at ON safety_incidents
    FOR EACH ROW EXECUTE FUNCTION set_sos_location_purge_at();
