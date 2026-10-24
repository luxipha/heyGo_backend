-- Stable slugs retain historical pricing and trip references. Legacy packages are
-- seeded only for compatibility; no speculative vehicle classification is seeded.
CREATE TABLE ride_categories (
 id UUID PRIMARY KEY, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
 description TEXT NOT NULL DEFAULT '', passengers INTEGER NOT NULL CHECK(passengers BETWEEN 1 AND 100),
 minimum_year INTEGER NOT NULL DEFAULT 1900 CHECK(minimum_year BETWEEN 1900 AND 2200),
 requires_ac BOOLEAN NOT NULL DEFAULT FALSE,
 status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','pending','active','inactive','archived')),
 legacy BOOLEAN NOT NULL DEFAULT FALSE, revision BIGINT NOT NULL DEFAULT 1,
 edited_by UUID REFERENCES admin_users(id), approved_by UUID REFERENCES admin_users(id),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX ride_category_name ON ride_categories(lower(regexp_replace(trim(name),'\s+',' ','g')));
INSERT INTO ride_categories(id,slug,name,passengers,status,legacy) VALUES
 ('00000000-0000-4000-8000-000000000001','bike','Bike',1,'active',TRUE),
 ('00000000-0000-4000-8000-000000000002','auto','Auto',3,'active',TRUE),
 ('00000000-0000-4000-8000-000000000003','sedan','Sedan',4,'active',TRUE),
 ('00000000-0000-4000-8000-000000000004','suv','SUV',6,'active',TRUE);
CREATE TABLE vehicle_catalogue (
 id UUID PRIMARY KEY, make TEXT NOT NULL, model TEXT NOT NULL, variant TEXT NOT NULL DEFAULT '',
 year_from INTEGER NOT NULL CHECK(year_from>=1900), year_to INTEGER NOT NULL CHECK(year_to>=year_from),
 body_type TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','active','archived')),
 revision BIGINT NOT NULL DEFAULT 1, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX vehicle_catalogue_identity ON vehicle_catalogue(lower(trim(make)),lower(trim(model)),lower(trim(variant)),year_from,year_to);
CREATE TABLE vehicle_category_models (
 category_id UUID NOT NULL REFERENCES ride_categories(id), model_id UUID NOT NULL REFERENCES vehicle_catalogue(id),
 PRIMARY KEY(category_id,model_id)
);
ALTER TABLE pricing_package_rates DROP CONSTRAINT pricing_package_rates_package_slug_check;
ALTER TABLE pricing_package_rates ADD CONSTRAINT pricing_category_slug_fk FOREIGN KEY(package_slug) REFERENCES ride_categories(slug);
ALTER TABLE driver_vehicles ADD COLUMN catalogue_model_id UUID REFERENCES vehicle_catalogue(id),
 ADD COLUMN passenger_seats INTEGER CHECK(passenger_seats BETWEEN 1 AND 100),
 ADD COLUMN working_ac BOOLEAN NOT NULL DEFAULT FALSE,
 ADD COLUMN condition_notes TEXT NOT NULL DEFAULT '';
CREATE TABLE driver_vehicle_categories (
 driver_id UUID NOT NULL REFERENCES driver_vehicles(driver_id) ON DELETE CASCADE, category_id UUID NOT NULL REFERENCES ride_categories(id),
 reviewed_by UUID REFERENCES admin_users(id), reviewed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY(driver_id,category_id)
);
CREATE FUNCTION driver_category_eligible(owner_id UUID, requested_slug TEXT) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM ride_categories c JOIN driver_vehicles v ON v.driver_id=owner_id
 WHERE c.slug=requested_slug AND c.status='active' AND v.review_status='approved' AND (
 (v.catalogue_model_id IS NULL AND c.legacy AND v.package_slug=c.slug) OR
 (v.catalogue_model_id IS NOT NULL AND EXISTS(SELECT 1 FROM driver_vehicle_categories a
 JOIN vehicle_catalogue m ON m.id=v.catalogue_model_id JOIN vehicle_category_models cm ON cm.model_id=m.id AND cm.category_id=c.id
 WHERE a.driver_id=v.driver_id AND a.category_id=c.id AND m.status='active'
 AND v.model_year BETWEEN m.year_from AND m.year_to AND v.model_year>=c.minimum_year
 AND v.passenger_seats>=c.passengers AND (NOT c.requires_ac OR v.working_ac)))))
$$;

ALTER TABLE driver_vehicles ADD COLUMN inspection_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE driver_inspections ADD COLUMN vehicle_revision BIGINT NOT NULL DEFAULT 0;

-- A report for an earlier car revision cannot activate a newly changed car.
CREATE OR REPLACE VIEW driver_eligibility AS
SELECT u.id AS driver_id,
    u.active AND (u.phone_verified_at IS NOT NULL OR u.email_verified)
    AND p.manual_verification_status = 'approved'
    AND p.admin_status = 'approved'
    AND p.admin_reviewed_at >= p.updated_at
    AND p.admin_reviewed_at >= COALESCE(v.updated_at, '-infinity'::TIMESTAMPTZ)
    AND p.admin_reviewed_at >= COALESCE((SELECT MAX(d.updated_at) FROM driver_documents d WHERE d.driver_id=u.id), '-infinity'::TIMESTAMPTZ)
    AND p.admin_reviewed_at >= COALESCE((SELECT MAX(i.updated_at) FROM driver_inspections i WHERE i.driver_id=u.id), '-infinity'::TIMESTAMPTZ)
    AND COALESCE((SELECT i.status = 'completed' AND i.vehicle_revision = COALESCE(v.inspection_revision,0) FROM driver_inspections i
                  WHERE i.driver_id = u.id ORDER BY i.created_at DESC, i.id DESC LIMIT 1), FALSE)
    AND NOT EXISTS (
        SELECT 1 FROM (VALUES ('driver_license'), ('vehicle_license'), ('roadworthiness'),
                            ('auto_insurance'), ('hackney_permit')) required(type)
        WHERE COALESCE((SELECT d.status = 'approved' AND (d.expires_at IS NULL OR d.expires_at > NOW())
                        FROM driver_documents d WHERE d.driver_id = u.id AND d.type = required.type
                        ORDER BY d.submitted_at DESC, d.id DESC LIMIT 1), FALSE) = FALSE
    ) AS eligible_to_drive
FROM users u JOIN driver_profiles p ON p.driver_id = u.id LEFT JOIN driver_vehicles v ON v.driver_id = u.id;
