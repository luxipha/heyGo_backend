-- Indexed channel history/latest reads and exact per-channel unread counts.
CREATE INDEX IF NOT EXISTS rider_notifications_channel_history_idx ON rider_notifications(rider_id,type,id DESC);
CREATE INDEX IF NOT EXISTS rider_notifications_channel_unread_idx ON rider_notifications(rider_id,type) WHERE read_at IS NULL;
