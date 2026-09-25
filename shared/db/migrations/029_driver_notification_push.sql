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
