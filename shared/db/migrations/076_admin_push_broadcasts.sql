CREATE TABLE admin_push_broadcasts (
 id UUID PRIMARY KEY,
 campaign_id UUID NOT NULL UNIQUE REFERENCES admin_campaign_drafts(id),
 source_version INTEGER NOT NULL,
 audience TEXT NOT NULL CHECK(audience IN ('Drivers','Riders','All users')),
 purpose TEXT NOT NULL CHECK(purpose IN ('announcement','marketing')),
 title TEXT NOT NULL,
 body TEXT NOT NULL,
 scheduled_for TIMESTAMPTZ,
 run_at TIMESTAMPTZ NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','scheduled','processing','enqueued','cancelled')),
 created_by UUID NOT NULL REFERENCES admin_users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE admin_push_recipients (
 broadcast_id UUID NOT NULL REFERENCES admin_push_broadcasts(id) ON DELETE CASCADE,
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 audience TEXT NOT NULL CHECK(audience IN ('driver','rider')),
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','enqueued','skipped')),
 PRIMARY KEY(broadcast_id,user_id)
);
ALTER TABLE driver_notification_preferences ADD COLUMN announcements BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE driver_notification_preferences ADD COLUMN promotions BOOLEAN NOT NULL DEFAULT FALSE;
CREATE OR REPLACE FUNCTION driver_notification_allowed(owner_id UUID, category TEXT) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE((SELECT CASE category WHEN 'dispatch' THEN p.ride_requests WHEN 'trip' THEN p.trip_updates WHEN 'earnings' THEN p.earnings WHEN 'balance' THEN p.earnings WHEN 'document_expiry' THEN p.document_reminders WHEN 'announcement' THEN p.announcements WHEN 'promotion' THEN p.promotions ELSE TRUE END FROM driver_notification_preferences p WHERE p.driver_id=owner_id),category<>'promotion')
$$;
CREATE INDEX admin_push_due ON admin_push_broadcasts(run_at) WHERE status IN ('queued','scheduled','processing');
