-- A purged coordinate intentionally retains its original source label.
ALTER TABLE sos_intakes DROP CONSTRAINT IF EXISTS sos_intakes_check1;
ALTER TABLE sos_intakes ADD CONSTRAINT sos_intakes_purged_location_check
    CHECK (location IS NOT NULL OR location_source='unavailable' OR location_purge_at IS NOT NULL);
