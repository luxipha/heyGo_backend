-- Identity remains in CasperID/users. These are HeyGo operational records.
CREATE TABLE IF NOT EXISTS driver_profiles (
    driver_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL DEFAULT '',
    photo_url TEXT NOT NULL DEFAULT '',
    admin_status TEXT NOT NULL DEFAULT 'pending' CHECK (admin_status IN ('pending', 'approved', 'rejected')),
    admin_reason TEXT NOT NULL DEFAULT '',
    admin_reviewed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS driver_vehicles (
    driver_id UUID PRIMARY KEY REFERENCES driver_profiles(driver_id) ON DELETE CASCADE,
    package_slug TEXT NOT NULL DEFAULT '',
    make TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    model_year INTEGER CHECK (model_year BETWEEN 1900 AND 2200),
    color TEXT NOT NULL DEFAULT '',
    plate TEXT NOT NULL DEFAULT '',
    review_status TEXT NOT NULL DEFAULT 'pending' CHECK (review_status IN ('pending', 'approved', 'rejected')),
    review_reason TEXT NOT NULL DEFAULT '',
    reviewed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS driver_documents (
    id UUID PRIMARY KEY,
    driver_id UUID NOT NULL REFERENCES driver_profiles(driver_id) ON DELETE CASCADE,
    type TEXT NOT NULL CHECK (type IN ('driver_license', 'vehicle_license', 'roadworthiness', 'auto_insurance', 'hackney_permit')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'under_review', 'approved', 'rejected')),
    rejection_reason TEXT NOT NULL DEFAULT '',
    storage_key TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::JSONB,
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS driver_documents_current_idx ON driver_documents(driver_id, type, submitted_at DESC);

CREATE TABLE IF NOT EXISTS driver_document_uploads (
    id UUID PRIMARY KEY,
    driver_id UUID NOT NULL REFERENCES driver_profiles(driver_id) ON DELETE CASCADE,
    type TEXT NOT NULL CHECK (type IN ('driver_license', 'vehicle_license', 'roadworthiness', 'auto_insurance', 'hackney_permit')),
    side TEXT NOT NULL CHECK (side IN ('document', 'front', 'back')),
    storage_key TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS driver_document_uploads_owner_idx ON driver_document_uploads(driver_id, created_at DESC);

CREATE TABLE IF NOT EXISTS driver_inspections (
    id UUID PRIMARY KEY,
    driver_id UUID NOT NULL REFERENCES driver_profiles(driver_id) ON DELETE CASCADE,
    partner_name TEXT NOT NULL DEFAULT '',
    location_name TEXT NOT NULL DEFAULT '',
    partner_reference TEXT NOT NULL DEFAULT '',
    report_storage_key TEXT,
    report_received_at TIMESTAMPTZ,
    status TEXT NOT NULL CHECK (status IN ('report_received', 'completed', 'failed')),
    outcome_reason TEXT NOT NULL DEFAULT '',
    inspected_at TIMESTAMPTZ,
    reviewed_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS driver_inspections_current_idx ON driver_inspections(driver_id, created_at DESC);

-- Defaults closed until CasperID supplies an explicit NIN verification proof.
ALTER TABLE users ADD COLUMN IF NOT EXISTS nin_verified BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS api_idempotency_keys (
    actor_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    method TEXT NOT NULL,
    path TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_status INTEGER,
    response_body JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (actor_id, key)
);

CREATE OR REPLACE VIEW driver_eligibility AS
SELECT u.id AS driver_id,
    u.verified AND u.nin_verified
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

CREATE TABLE IF NOT EXISTS user_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    recipient_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_event_id TEXT NOT NULL,
    type TEXT NOT NULL,
    data JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(recipient_id, source_event_id, type)
);
CREATE INDEX IF NOT EXISTS user_events_recipient_idx ON user_events(recipient_id, id);
