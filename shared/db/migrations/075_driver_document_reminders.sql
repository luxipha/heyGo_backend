ALTER TABLE driver_notification_preferences ADD COLUMN document_reminders BOOLEAN NOT NULL DEFAULT TRUE;
CREATE OR REPLACE FUNCTION driver_notification_allowed(owner_id UUID, category TEXT) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE((SELECT CASE category WHEN 'dispatch' THEN p.ride_requests WHEN 'trip' THEN p.trip_updates WHEN 'earnings' THEN p.earnings WHEN 'balance' THEN p.earnings WHEN 'document_expiry' THEN p.document_reminders ELSE TRUE END FROM driver_notification_preferences p WHERE p.driver_id=owner_id),TRUE)
$$;
CREATE TABLE driver_document_reminders (
 document_id UUID NOT NULL REFERENCES driver_documents(id) ON DELETE CASCADE,
 expires_at TIMESTAMPTZ NOT NULL,
 stage TEXT NOT NULL CHECK(stage IN ('30_days','7_days','1_day','expired')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(document_id,expires_at,stage)
);

CREATE FUNCTION driver_document_reminder_current(payload JSONB) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM driver_documents d WHERE d.id=(payload->>'documentId')::UUID AND d.expires_at=(payload->>'expiresAt')::TIMESTAMPTZ AND d.status='approved'
 AND NOT EXISTS(SELECT 1 FROM driver_documents newer WHERE newer.driver_id=d.driver_id AND newer.type=d.type AND (newer.submitted_at,newer.id)>(d.submitted_at,d.id)))
$$;
