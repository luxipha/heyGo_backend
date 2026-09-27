ALTER TABLE payments
    ADD COLUMN IF NOT EXISTS initialization_token UUID,
    ADD COLUMN IF NOT EXISTS initialization_lease_until TIMESTAMPTZ;

CREATE OR REPLACE FUNCTION notify_heygo_user_event()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('heygo_user_events', NEW.recipient_id::TEXT);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS user_events_notify_gateway ON user_events;
CREATE TRIGGER user_events_notify_gateway
AFTER INSERT ON user_events
FOR EACH ROW EXECUTE FUNCTION notify_heygo_user_event();
