ALTER TABLE users
    ALTER COLUMN casper_id_user_id DROP NOT NULL,
    ALTER COLUMN human_id DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS phone_e164 TEXT,
    ADD COLUMN IF NOT EXISTS phone_verified_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS email TEXT,
    ADD COLUMN IF NOT EXISTS email_verified BOOLEAN NOT NULL DEFAULT FALSE;

CREATE UNIQUE INDEX IF NOT EXISTS users_phone_e164_unique
    ON users(phone_e164) WHERE phone_e164 IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS users_email_unique
    ON users(LOWER(email)) WHERE email IS NOT NULL;

CREATE TABLE IF NOT EXISTS auth_otp_challenges (
    id UUID PRIMARY KEY,
    channel TEXT NOT NULL CHECK (channel IN ('phone','email')),
    destination TEXT NOT NULL,
    code_hash TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS auth_otp_challenges_destination_idx
    ON auth_otp_challenges(channel, destination, created_at DESC);

CREATE TABLE IF NOT EXISTS user_sessions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    access_token_hash TEXT NOT NULL UNIQUE,
    refresh_token_hash TEXT NOT NULL UNIQUE,
    access_expires_at TIMESTAMPTZ NOT NULL,
    refresh_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    device_name TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (refresh_expires_at > access_expires_at)
);
CREATE INDEX IF NOT EXISTS user_sessions_user_active_idx
    ON user_sessions(user_id, refresh_expires_at DESC) WHERE revoked_at IS NULL;

ALTER TABLE driver_profiles
    ADD COLUMN IF NOT EXISTS manual_verification_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (manual_verification_status IN ('pending','approved','rejected')),
    ADD COLUMN IF NOT EXISTS manual_verification_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS manual_verified_by UUID REFERENCES admin_users(id),
    ADD COLUMN IF NOT EXISTS manual_verified_at TIMESTAMPTZ;

ALTER TABLE driver_account_deletion_requests
    ALTER COLUMN casper_subject_hash DROP NOT NULL,
    ALTER COLUMN human_id_hash DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS phone_hash TEXT,
    ADD COLUMN IF NOT EXISTS email_hash TEXT;

CREATE TABLE IF NOT EXISTS deactivated_phone_identities (
    phone_hash TEXT PRIMARY KEY,
    deletion_request_id UUID NOT NULL UNIQUE REFERENCES driver_account_deletion_requests(id),
    deactivated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS deactivated_email_identities (
    email_hash TEXT PRIMARY KEY,
    deletion_request_id UUID NOT NULL UNIQUE REFERENCES driver_account_deletion_requests(id),
    deactivated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE OR REPLACE VIEW driver_eligibility AS
SELECT u.id AS driver_id,
    u.active AND (u.phone_verified_at IS NOT NULL OR u.email_verified)
    AND p.manual_verification_status = 'approved'
    AND p.admin_status = 'approved'
    AND p.admin_reviewed_at >= p.updated_at
    AND p.admin_reviewed_at >= COALESCE(v.updated_at, '-infinity'::TIMESTAMPTZ)
    AND p.admin_reviewed_at >= COALESCE((SELECT MAX(d.updated_at) FROM driver_documents d WHERE d.driver_id=u.id), '-infinity'::TIMESTAMPTZ)
    AND p.admin_reviewed_at >= COALESCE((SELECT MAX(i.updated_at) FROM driver_inspections i WHERE i.driver_id=u.id), '-infinity'::TIMESTAMPTZ)
    AND COALESCE((SELECT i.status = 'completed' FROM driver_inspections i
                  WHERE i.driver_id = u.id ORDER BY i.created_at DESC, i.id DESC LIMIT 1), FALSE)
    AND NOT EXISTS (
        SELECT 1 FROM (VALUES ('driver_license'), ('vehicle_license'), ('roadworthiness'),
                            ('auto_insurance'), ('hackney_permit')) required(type)
        WHERE COALESCE((SELECT d.status = 'approved' AND (d.expires_at IS NULL OR d.expires_at > NOW())
                        FROM driver_documents d WHERE d.driver_id = u.id AND d.type = required.type
                        ORDER BY d.submitted_at DESC, d.id DESC LIMIT 1), FALSE) = FALSE
    ) AS eligible_to_drive
FROM users u JOIN driver_profiles p ON p.driver_id = u.id LEFT JOIN driver_vehicles v ON v.driver_id = u.id;
