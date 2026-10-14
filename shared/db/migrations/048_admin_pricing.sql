-- A published market rate is required for every new quote. Rates use kobo,
-- metres, and minutes explicitly to avoid implicit unit conversions.
CREATE TABLE pricing_versions (
    id UUID PRIMARY KEY,
    market_code TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','approved')),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_until TIMESTAMPTZ,
    created_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    approved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(market_code,version),
    CHECK (effective_until IS NULL OR effective_until>effective_from),
    CHECK ((status='approved')=(approved_by IS NOT NULL AND approved_at IS NOT NULL)),
    CHECK (approved_by IS NULL OR approved_by<>created_by)
);
CREATE INDEX pricing_versions_active_idx ON pricing_versions(market_code,status,effective_from,effective_until);

CREATE TABLE pricing_package_rates (
    pricing_id UUID NOT NULL REFERENCES pricing_versions(id),
    package_slug TEXT NOT NULL CHECK (package_slug IN ('bike','auto','sedan','suv')),
    base_kobo BIGINT NOT NULL CHECK (base_kobo BETWEEN 0 AND 1000000),
    per_km_kobo BIGINT NOT NULL CHECK (per_km_kobo BETWEEN 0 AND 10000),
    per_minute_kobo BIGINT NOT NULL CHECK (per_minute_kobo BETWEEN 0 AND 10000),
    PRIMARY KEY(pricing_id,package_slug)
);

CREATE TABLE pricing_retirements (
    id UUID PRIMARY KEY,
    pricing_id UUID NOT NULL REFERENCES pricing_versions(id),
    effective_until TIMESTAMPTZ NOT NULL,
    requested_by UUID NOT NULL REFERENCES admin_users(id),
    approved_by UUID REFERENCES admin_users(id),
    approved_at TIMESTAMPTZ,
    status TEXT NOT NULL CHECK (status IN ('pending','approved')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((status='approved')=(approved_by IS NOT NULL AND approved_at IS NOT NULL)),
    CHECK (approved_by IS NULL OR approved_by<>requested_by)
);
CREATE UNIQUE INDEX pricing_pending_retirement_idx ON pricing_retirements(pricing_id) WHERE status='pending';

CREATE FUNCTION protect_pricing_version() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP='DELETE' AND OLD.status='approved' THEN
        RAISE EXCEPTION 'approved pricing cannot be deleted';
    END IF;
    IF TG_OP='DELETE' THEN
        RETURN OLD;
    END IF;
    IF TG_OP='UPDATE' AND OLD.status='approved' THEN
        IF NEW.id IS DISTINCT FROM OLD.id OR NEW.market_code IS DISTINCT FROM OLD.market_code
          OR NEW.version IS DISTINCT FROM OLD.version OR NEW.status IS DISTINCT FROM OLD.status
          OR NEW.effective_from IS DISTINCT FROM OLD.effective_from OR NEW.created_by IS DISTINCT FROM OLD.created_by
          OR NEW.approved_by IS DISTINCT FROM OLD.approved_by OR NEW.approved_at IS DISTINCT FROM OLD.approved_at
          OR NEW.created_at IS DISTINCT FROM OLD.created_at OR OLD.effective_until IS NOT NULL
          OR NEW.effective_until IS NULL OR NEW.effective_until<=NOW() THEN
            RAISE EXCEPTION 'approved pricing is immutable except future retirement';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER pricing_versions_protect BEFORE UPDATE OR DELETE ON pricing_versions
    FOR EACH ROW EXECUTE FUNCTION protect_pricing_version();

CREATE FUNCTION protect_published_pricing_rate() RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM pricing_versions WHERE id=OLD.pricing_id AND status='approved') THEN
        RAISE EXCEPTION 'published pricing rates are immutable';
    END IF;
    RETURN COALESCE(NEW,OLD);
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER pricing_rates_protect BEFORE UPDATE OR DELETE ON pricing_package_rates
    FOR EACH ROW EXECUTE FUNCTION protect_published_pricing_rate();

ALTER TABLE ride_fares ADD COLUMN pricing_version_id UUID REFERENCES pricing_versions(id);
ALTER TABLE ride_fares ADD COLUMN pricing_market_code TEXT;
CREATE INDEX ride_fares_pricing_version_idx ON ride_fares(pricing_version_id);
