CREATE TABLE IF NOT EXISTS driver_notifications (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    driver_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_event_id TEXT NOT NULL,
    type TEXT NOT NULL,
    title TEXT NOT NULL,
    message TEXT NOT NULL,
    data JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    read_at TIMESTAMPTZ,
    UNIQUE(driver_id, source_event_id)
);
CREATE INDEX IF NOT EXISTS driver_notifications_inbox_idx
    ON driver_notifications(driver_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS driver_notifications_unread_idx
    ON driver_notifications(driver_id, id DESC) WHERE read_at IS NULL;

CREATE TABLE IF NOT EXISTS driver_notification_push_deliveries (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    notification_id BIGINT NOT NULL REFERENCES driver_notifications(id) ON DELETE CASCADE,
    device_id UUID NOT NULL REFERENCES driver_devices(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','sent','dead')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_until TIMESTAMPTZ,
    sent_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(notification_id, device_id)
);
CREATE INDEX IF NOT EXISTS driver_notification_push_pending_idx
    ON driver_notification_push_deliveries(next_attempt_at, id)
    WHERE status IN ('pending','processing');
