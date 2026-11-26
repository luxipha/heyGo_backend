-- Admin-configurable driver requirements.
--
-- Identity (NIN) and the driver's licence are always required. The vehicle
-- papers and the vehicle inspection can each be Required, Optional or Off.
--   required: must be approved before the driver can go online
--   optional: shown and reviewed, but never blocks going online
--   off:      not asked for
-- When a requirement becomes Required, drivers who registered BEFORE the change
-- get `grace_days` before it blocks them (drivers who register later follow the
-- new standard from the start). required_since records when it last became
-- required; it stays NULL until the first change, so there is no grace on the
-- initial defaults.
CREATE TABLE driver_requirement_config (
    type TEXT PRIMARY KEY CHECK (type IN ('vehicle_license','roadworthiness','auto_insurance','hackney_permit','vehicle_inspection')),
    mode TEXT NOT NULL CHECK (mode IN ('required','optional','off')),
    grace_days INTEGER NOT NULL DEFAULT 14 CHECK (grace_days BETWEEN 0 AND 365),
    required_since TIMESTAMPTZ,
    updated_by UUID,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO driver_requirement_config(type, mode) VALUES
    ('vehicle_license', 'required'),
    ('auto_insurance', 'required'),
    ('roadworthiness', 'optional'),
    ('hackney_permit', 'off'),
    ('vehicle_inspection', 'off');

-- True when `kind` currently blocks this driver: it is Required and the driver
-- is not inside the grace window that follows a switch to Required.
CREATE FUNCTION requirement_enforced(owner_id UUID, kind TEXT) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE((
  SELECT c.mode='required' AND NOT (
   c.required_since IS NOT NULL
   AND COALESCE((SELECT u.created_at FROM users u WHERE u.id=owner_id), NOW()) < c.required_since
   AND NOW() < c.required_since + make_interval(days => c.grace_days))
  FROM driver_requirement_config c WHERE c.type=kind), FALSE)
$$;

-- The vehicle part of the online gate now follows the configuration.
CREATE OR REPLACE FUNCTION reviewed_vehicle_ready(owner_id UUID, car_id UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM driver_vehicle_records v WHERE v.id=car_id AND v.driver_id=owner_id AND v.review_status='approved' AND v.approval_status='approved' AND v.approved_at>=v.updated_at
 AND (NOT requirement_enforced(owner_id,'vehicle_inspection') OR COALESCE((SELECT i.status='completed' AND i.vehicle_revision=v.inspection_revision AND v.approved_at>=i.updated_at FROM driver_inspections_records i WHERE i.vehicle_id=v.id ORDER BY i.created_at DESC,i.id DESC LIMIT 1),FALSE))
 AND NOT EXISTS(SELECT 1 FROM (VALUES('vehicle_license'),('roadworthiness'),('auto_insurance'),('hackney_permit')) required(type) WHERE requirement_enforced(owner_id,required.type) AND NOT COALESCE((SELECT d.status='approved' AND (d.expires_at IS NULL OR d.expires_at>NOW()) AND v.approved_at>=d.updated_at FROM driver_documents_records d WHERE d.vehicle_id=v.id AND d.type=required.type ORDER BY d.submitted_at DESC,d.id DESC LIMIT 1),FALSE)))
$$;

-- One reminder per driver, item, change and stage, so the job never repeats.
CREATE TABLE driver_requirement_reminders (
    driver_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    required_since TIMESTAMPTZ NOT NULL,
    stage TEXT NOT NULL CHECK (stage IN ('announce','7_days','1_day')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (driver_id, type, required_since, stage)
);

-- Inspection evidence versions (driver_inspections.evidence_version):
--   0 legacy, 1 photos + walkaround video, 2 report only (the report heyGo staff
--   upload for the driver; no driver photos or video). New staff-uploaded reports
--   use 2. No schema change is needed; the column has no CHECK.
