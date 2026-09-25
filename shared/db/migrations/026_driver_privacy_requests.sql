ALTER TABLE users ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deactivated_at TIMESTAMPTZ;

CREATE OR REPLACE VIEW driver_eligibility AS
SELECT u.id AS driver_id,
    u.active AND u.verified AND u.nin_verified
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

CREATE TABLE IF NOT EXISTS driver_account_deletion_requests (
    id UUID PRIMARY KEY,
    driver_id UUID NOT NULL REFERENCES users(id),
    casper_subject_hash TEXT NOT NULL,
    human_id_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_by UUID REFERENCES admin_users(id),
    reviewed_at TIMESTAMPTZ,
    review_reason TEXT NOT NULL DEFAULT '',
    completed_at TIMESTAMPTZ,
    CONSTRAINT driver_account_deletion_review_consistency CHECK (
        (status='pending' AND reviewed_by IS NULL AND reviewed_at IS NULL AND completed_at IS NULL)
        OR (status='rejected' AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL AND completed_at IS NULL)
        OR (status='approved' AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL)
    )
);
CREATE TABLE IF NOT EXISTS deactivated_casperid_identities (
    subject_hash TEXT PRIMARY KEY,
    human_id_hash TEXT NOT NULL UNIQUE,
    deletion_request_id UUID NOT NULL UNIQUE REFERENCES driver_account_deletion_requests(id),
    deactivated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS driver_account_deletion_one_pending_idx
    ON driver_account_deletion_requests(driver_id) WHERE status='pending';
CREATE INDEX IF NOT EXISTS driver_account_deletion_queue_idx
    ON driver_account_deletion_requests(status, requested_at DESC);

CREATE TABLE IF NOT EXISTS driver_account_deletion_files (
    request_id UUID NOT NULL REFERENCES driver_account_deletion_requests(id),
    storage_key TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_until TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(request_id, storage_key)
);
CREATE INDEX IF NOT EXISTS driver_account_deletion_files_pending_idx
    ON driver_account_deletion_files(next_attempt_at) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS driver_data_exports (
    id UUID PRIMARY KEY,
    driver_id UUID NOT NULL REFERENCES users(id),
    requested_email TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','sent','failed','expired','cancelled')),
    archive_storage_key TEXT,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT driver_data_exports_lifecycle CHECK (
        (status='sent' AND archive_storage_key IS NOT NULL AND sent_at IS NOT NULL AND expires_at IS NOT NULL)
        OR status <> 'sent'
    )
);
CREATE UNIQUE INDEX IF NOT EXISTS driver_data_exports_one_pending_idx
    ON driver_data_exports(driver_id) WHERE status IN ('pending','processing');
CREATE INDEX IF NOT EXISTS driver_data_exports_expiry_idx
    ON driver_data_exports(expires_at) WHERE status='sent';

CREATE TABLE IF NOT EXISTS admin_resend_settings (
    id SMALLINT PRIMARY KEY CHECK (id=1),
    api_key_ciphertext BYTEA NOT NULL,
    from_email TEXT NOT NULL,
    from_name TEXT NOT NULL DEFAULT 'HeyGo',
    updated_by UUID NOT NULL REFERENCES admin_users(id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
