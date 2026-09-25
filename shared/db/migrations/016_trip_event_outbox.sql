-- The trip transition and its notification must commit together. A publisher
-- retries unpublished rows with the same event ID after a crash or broker error.
CREATE TABLE IF NOT EXISTS trip_event_outbox (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    trip_id UUID NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    topic TEXT NOT NULL,
    recipient_id UUID NOT NULL REFERENCES users(id),
    payload JSONB NOT NULL,
    correlation_id TEXT,
    attempt INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ,
    UNIQUE (trip_id, topic, recipient_id, attempt)
);
CREATE INDEX IF NOT EXISTS trip_event_outbox_pending_idx ON trip_event_outbox(id) WHERE published_at IS NULL;

-- The assignment and its expiry/decline outcomes are committed together.
-- A retry event is consumed by driver-service even if its worker crashes just
-- after closing an offer. Repeated Kafka delivery is guarded by trip status.
CREATE OR REPLACE FUNCTION queue_driver_assignment_events() RETURNS TRIGGER AS $$
DECLARE
    rider UUID;
    previous_driver UUID;
BEGIN
    SELECT rider_id INTO rider FROM trips WHERE id=NEW.trip_id;
    IF TG_OP = 'INSERT' THEN
      IF NEW.attempt > 1 THEN
        SELECT driver_id INTO previous_driver FROM driver_assignments
        WHERE trip_id=NEW.trip_id AND attempt<NEW.attempt ORDER BY attempt DESC LIMIT 1;
        IF previous_driver IS NOT NULL THEN
            INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,attempt)
            VALUES(NEW.trip_id,'trip.event.reassigned',previous_driver,
                jsonb_build_object('tripId',NEW.trip_id,'status','reassigned'),NEW.attempt)
            ON CONFLICT DO NOTHING;
        END IF;
        INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,attempt)
        VALUES(NEW.trip_id,'trip.event.reassigned',rider,
            jsonb_build_object('tripId',NEW.trip_id,'status','reassigned'),NEW.attempt)
        ON CONFLICT DO NOTHING;
      END IF;
      RETURN NEW;
    END IF;
    IF OLD.status='offered' AND NEW.status IN ('declined','timed_out') THEN
        IF NEW.status='declined' THEN
            INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,attempt)
            VALUES(NEW.trip_id,'driver.event.command_acknowledged',NEW.driver_id,
                jsonb_build_object('command','driver.cmd.trip_decline','tripId',NEW.trip_id,'status','declined'),NEW.attempt)
            ON CONFLICT DO NOTHING;
        ELSE
            INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,attempt)
            VALUES(NEW.trip_id,'trip.event.expired',NEW.driver_id,
                jsonb_build_object('tripId',NEW.trip_id,'status','expired'),NEW.attempt)
            ON CONFLICT DO NOTHING;
        END IF;
        INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,attempt)
        VALUES(NEW.trip_id,'trip.event.driver_not_interested',rider,NEW.trip_payload,NEW.attempt)
        ON CONFLICT DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS driver_assignment_events_outbox ON driver_assignments;
CREATE TRIGGER driver_assignment_events_outbox
AFTER INSERT OR UPDATE OF status ON driver_assignments
FOR EACH ROW EXECUTE FUNCTION queue_driver_assignment_events();
