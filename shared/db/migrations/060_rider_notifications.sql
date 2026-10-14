CREATE TABLE rider_notification_preferences (
 rider_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 ride_updates BOOLEAN NOT NULL DEFAULT TRUE,
 account_alerts BOOLEAN NOT NULL DEFAULT TRUE,
 promotions BOOLEAN NOT NULL DEFAULT FALSE,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE rider_devices (
 id UUID PRIMARY KEY,
 rider_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 session_id UUID NOT NULL REFERENCES user_sessions(id) ON DELETE CASCADE,
 platform TEXT NOT NULL CHECK(platform IN ('ios','android')),
 push_token TEXT NOT NULL,
 app_version TEXT NOT NULL DEFAULT '',
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX rider_device_active_token ON rider_devices(push_token) WHERE enabled;
CREATE INDEX rider_devices_owner ON rider_devices(rider_id);
CREATE TABLE rider_notifications (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 rider_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 source_event_id TEXT NOT NULL,
 type TEXT NOT NULL CHECK(type IN ('ride_updates','account_alerts','promotions')),
 title TEXT NOT NULL,
 message TEXT NOT NULL,
 data JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 read_at TIMESTAMPTZ,
 UNIQUE(rider_id,source_event_id)
);
CREATE INDEX rider_notifications_owner ON rider_notifications(rider_id,id DESC);
CREATE INDEX rider_notifications_unread ON rider_notifications(rider_id,id) WHERE read_at IS NULL;
CREATE TABLE rider_notification_push_deliveries (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 notification_id BIGINT NOT NULL REFERENCES rider_notifications(id) ON DELETE CASCADE,
 device_id UUID NOT NULL REFERENCES rider_devices(id) ON DELETE CASCADE,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','processing','sent','dead')),
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 lease_until TIMESTAMPTZ,
 sent_at TIMESTAMPTZ,
 last_error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(notification_id,device_id)
);
CREATE INDEX rider_push_pending ON rider_notification_push_deliveries(next_attempt_at,id) WHERE status IN ('pending','processing');
