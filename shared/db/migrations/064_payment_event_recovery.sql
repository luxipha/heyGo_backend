ALTER TABLE payment_webhook_events
    ADD COLUMN IF NOT EXISTS topic TEXT,
    ADD COLUMN IF NOT EXISTS entity_id TEXT,
    ADD COLUMN IF NOT EXISTS payload JSONB;

-- Recover only legacy events whose exact ID and terminal outcome match the
-- stored webhook. Unmatched rows remain pending for operator investigation.
UPDATE payment_webhook_events e
SET topic = CASE p.status
        WHEN 'success' THEN 'payment.event.success'
        WHEN 'failed' THEN 'payment.event.failed'
        WHEN 'cancelled' THEN 'payment.event.cancelled'
    END,
    entity_id = p.rider_id,
    payload = jsonb_build_object(
        'eventID', e.event_id, 'tripID', p.trip_id, 'riderID', p.rider_id,
        'driverID', p.driver_id, 'paymentReference', p.payment_reference,
        'transactionReference', p.transaction_reference, 'status', p.status)
FROM payments p
WHERE NOT e.published AND e.payload IS NULL
    AND p.status IN ('success', 'failed', 'cancelled')
    AND e.event_id = (p.provider_metadata->'webhook'->>'eventType')
        || ':' || p.transaction_reference || ':' || p.status;

CREATE INDEX IF NOT EXISTS payment_webhook_events_pending_idx
    ON payment_webhook_events (processed_at, event_id) WHERE NOT published;
