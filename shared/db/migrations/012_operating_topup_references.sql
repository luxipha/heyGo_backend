-- Merchant payment references identify a checkout; Monnify transaction
-- references identify the payment that may be credited exactly once.
ALTER TABLE driver_operating_topups ADD COLUMN IF NOT EXISTS provider_transaction_reference TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS driver_operating_topups_transaction_idx
    ON driver_operating_topups(provider_transaction_reference) WHERE provider_transaction_reference IS NOT NULL;
