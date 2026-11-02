CREATE TABLE driver_phone_challenges (
 id UUID PRIMARY KEY,
 driver_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 destination TEXT NOT NULL,
 previous_phone TEXT NOT NULL DEFAULT '',
 code_hash TEXT NOT NULL,
 attempts INT NOT NULL DEFAULT 0,
 expires_at TIMESTAMPTZ NOT NULL,
 consumed_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX driver_phone_challenges_owner ON driver_phone_challenges(driver_id,created_at);
CREATE INDEX driver_phone_challenges_destination ON driver_phone_challenges(destination,created_at);

-- All matching and GPS availability paths share this gate; email login remains valid.
CREATE OR REPLACE VIEW driver_eligibility AS SELECT u.id AS driver_id,u.active AND (u.phone_verified_at IS NOT NULL AND COALESCE(u.phone_e164,'')<>'') AND p.manual_verification_status='approved' AND p.admin_status='approved' AND p.admin_reviewed_at>=p.updated_at AND reviewed_vehicle_ready(u.id,selected_driver_vehicle(u.id)) AND COALESCE((SELECT d.status='approved' AND (d.expires_at IS NULL OR d.expires_at>NOW()) AND p.admin_reviewed_at>=d.updated_at FROM driver_documents_records d WHERE d.driver_id=u.id AND d.type='driver_license' AND d.vehicle_id IS NULL ORDER BY d.submitted_at DESC,d.id DESC LIMIT 1),FALSE) AS eligible_to_drive FROM users u JOIN driver_profiles p ON p.driver_id=u.id;
