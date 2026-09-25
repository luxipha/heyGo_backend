-- Admin-owned, versioned boundaries. Approved rows are immutable snapshots;
-- a boundary change is a new version with its own effective interval.
CREATE TABLE IF NOT EXISTS trip_geofences (
    id UUID PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('market', 'region', 'airport')),
    code TEXT NOT NULL,
    name TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    boundary GEOMETRY(MULTIPOLYGON, 4326) NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft', 'approved')),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_until TIMESTAMPTZ,
    created_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    approved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (kind, code, version),
    CHECK (effective_until IS NULL OR effective_until > effective_from),
    CHECK (ST_IsValid(boundary)),
    CHECK ((status = 'approved') = (approved_by IS NOT NULL AND approved_at IS NOT NULL)),
    CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE INDEX IF NOT EXISTS trip_geofences_boundary_gist ON trip_geofences USING GIST (boundary);
CREATE INDEX IF NOT EXISTS trip_geofences_active_idx ON trip_geofences (kind, status, effective_from, effective_until);

CREATE OR REPLACE FUNCTION protect_approved_trip_geofence() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status = 'approved' THEN
            RAISE EXCEPTION 'approved geofence versions cannot be deleted';
        END IF;
        RETURN OLD;
    END IF;
    IF OLD.status = 'approved' THEN
        IF NEW.id IS DISTINCT FROM OLD.id OR NEW.kind IS DISTINCT FROM OLD.kind
           OR NEW.code IS DISTINCT FROM OLD.code OR NEW.name IS DISTINCT FROM OLD.name
           OR NEW.version IS DISTINCT FROM OLD.version OR NEW.boundary IS DISTINCT FROM OLD.boundary
           OR NEW.status IS DISTINCT FROM OLD.status OR NEW.effective_from IS DISTINCT FROM OLD.effective_from
           OR NEW.created_by IS DISTINCT FROM OLD.created_by OR NEW.approved_by IS DISTINCT FROM OLD.approved_by
           OR NEW.approved_at IS DISTINCT FROM OLD.approved_at OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
            RAISE EXCEPTION 'approved geofence versions are immutable';
        END IF;
        IF NEW.effective_until IS DISTINCT FROM OLD.effective_until
           AND (OLD.effective_until IS NOT NULL OR NEW.effective_until IS NULL OR NEW.effective_until < NOW()) THEN
            RAISE EXCEPTION 'approved geofences may only be retired once, now or in the future';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trip_geofences_protect_approved
    BEFORE UPDATE OR DELETE ON trip_geofences
    FOR EACH ROW EXECUTE FUNCTION protect_approved_trip_geofence();

CREATE TABLE IF NOT EXISTS trip_geofence_retirements (
    id UUID PRIMARY KEY,
    geofence_id UUID NOT NULL REFERENCES trip_geofences(id),
    effective_until TIMESTAMPTZ NOT NULL,
    requested_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at TIMESTAMPTZ,
    CHECK ((approved_by IS NULL) = (approved_at IS NULL)),
    CHECK (approved_by IS NULL OR approved_by <> requested_by)
);
CREATE UNIQUE INDEX IF NOT EXISTS trip_geofence_pending_retirement_idx
    ON trip_geofence_retirements(geofence_id) WHERE status='pending';

-- Historical trips keep their classification even when Admin publishes a new
-- boundary. The JSON snapshot records the exact geofence IDs and versions.
ALTER TABLE trips ADD COLUMN IF NOT EXISTS market_code TEXT;
ALTER TABLE trips ADD COLUMN IF NOT EXISTS origin_region_code TEXT;
ALTER TABLE trips ADD COLUMN IF NOT EXISTS destination_region_code TEXT;
ALTER TABLE trips ADD COLUMN IF NOT EXISTS regulatory_tags TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE trips ADD COLUMN IF NOT EXISTS classification_geofences JSONB;
ALTER TABLE trips ADD COLUMN IF NOT EXISTS classified_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS trips_market_created_idx ON trips (market_code, created_at DESC);
