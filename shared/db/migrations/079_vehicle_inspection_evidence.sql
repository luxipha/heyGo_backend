-- Preserve unknown values; existing FALSE cannot distinguish failure from unassessed.
ALTER TABLE driver_vehicles ALTER COLUMN working_ac DROP NOT NULL;
ALTER TABLE driver_vehicles ALTER COLUMN working_ac DROP DEFAULT;
UPDATE driver_vehicles SET working_ac=NULL WHERE working_ac=FALSE;
ALTER TABLE driver_vehicles ADD COLUMN findings JSONB NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(findings)='object');
ALTER TABLE driver_inspections ADD COLUMN vehicle_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb;
-- Legacy inspections remain historical. New reports explicitly use evidence_version=1.
ALTER TABLE driver_inspections ADD COLUMN evidence_version INTEGER NOT NULL DEFAULT 0;
CREATE TABLE vehicle_photo_uploads (
 id UUID PRIMARY KEY, driver_id UUID NOT NULL REFERENCES driver_profiles(driver_id) ON DELETE CASCADE,
 actor_id UUID NOT NULL, actor_type TEXT NOT NULL CHECK(actor_type IN ('admin','driver')), vehicle_revision BIGINT NOT NULL,
 angle TEXT NOT NULL CHECK(angle IN ('front','rear','left','right','front_interior','rear_interior','vin','walkaround')),
 storage_key TEXT NOT NULL UNIQUE, content_type TEXT NOT NULL CHECK(content_type IN ('image/jpeg','image/png','video/mp4','video/quicktime')),
 size_bytes BIGINT NOT NULL CHECK(size_bytes BETWEEN 1 AND 209715200),
 expires_at TIMESTAMPTZ NOT NULL, consumed_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE driver_vehicle_photos (
 id UUID PRIMARY KEY, driver_id UUID NOT NULL REFERENCES driver_profiles(driver_id) ON DELETE CASCADE,
 vehicle_revision BIGINT NOT NULL, angle TEXT NOT NULL CHECK(angle IN ('front','rear','left','right','front_interior','rear_interior','vin','walkaround')),
 storage_key TEXT NOT NULL, content_type TEXT NOT NULL, size_bytes BIGINT NOT NULL,
 uploaded_by UUID NOT NULL, uploader_type TEXT NOT NULL CHECK(uploader_type IN ('admin','driver')), created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), superseded_at TIMESTAMPTZ
);
CREATE INDEX driver_vehicle_photos_owner ON driver_vehicle_photos(driver_id,vehicle_revision);

CREATE UNIQUE INDEX driver_vehicle_photos_current ON driver_vehicle_photos(driver_id,vehicle_revision,angle) WHERE superseded_at IS NULL;
