-- Riders may pay an approved no-show debt directly to HeyGo. Provider
-- verification is required before the claimant's Operating Balance is credited.
ALTER TABLE rider_no_show_debts DROP CONSTRAINT IF EXISTS rider_no_show_debts_status_check;
ALTER TABLE rider_no_show_debts ADD CONSTRAINT rider_no_show_debts_status_check
    CHECK (status IN ('due','collection_pending','settled'));

ALTER TABLE trip_no_show_debt_settlements DROP CONSTRAINT IF EXISTS trip_no_show_debt_settlements_status_check;
ALTER TABLE trip_no_show_debt_settlements ADD CONSTRAINT trip_no_show_debt_settlements_status_check
    CHECK (status IN ('pending','settled','released','collection_pending'));
CREATE TABLE IF NOT EXISTS rider_no_show_debt_payments (
    id UUID PRIMARY KEY,
    debt_id UUID NOT NULL REFERENCES rider_no_show_debts(id),
    rider_id UUID NOT NULL REFERENCES users(id),
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo > 0),
    provider TEXT NOT NULL CHECK (provider='monnify'),
    provider_reference TEXT NOT NULL UNIQUE,
    provider_transaction_reference TEXT UNIQUE,
    checkout_url TEXT,
    idempotency_key TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','succeeded','failed')),
    operating_entry_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    confirmed_at TIMESTAMPTZ,
    CHECK ((status='succeeded')=(confirmed_at IS NOT NULL)),
    CHECK ((status='succeeded')=(operating_entry_id IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS rider_no_show_debt_one_pending_payment_idx
    ON rider_no_show_debt_payments(debt_id) WHERE status='pending';
CREATE UNIQUE INDEX IF NOT EXISTS rider_no_show_debt_payment_idempotency_idx
    ON rider_no_show_debt_payments(rider_id,idempotency_key);
ALTER TABLE rider_no_show_debt_payments
    ADD CONSTRAINT rider_no_show_debt_payments_entry_fk FOREIGN KEY(operating_entry_id) REFERENCES driver_operating_entries(id);

-- Recreate the guard after widening the set of live allocation states.
DROP INDEX IF EXISTS trip_no_show_debt_one_open_allocation_idx;
CREATE UNIQUE INDEX trip_no_show_debt_one_open_allocation_idx
    ON trip_no_show_debt_settlements(debt_id) WHERE status IN ('pending','settled','collection_pending');
