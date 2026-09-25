-- Older approved policies remain readable. Top-up checkout stays unavailable
-- for them until Admin publishes a new version with explicit bounds.
ALTER TABLE operating_balance_policies ADD COLUMN IF NOT EXISTS topup_minimum_kobo BIGINT;
ALTER TABLE operating_balance_policies ADD COLUMN IF NOT EXISTS topup_maximum_kobo BIGINT;
ALTER TABLE operating_balance_policies ADD CONSTRAINT operating_balance_topup_bounds
    CHECK ((topup_minimum_kobo IS NULL AND topup_maximum_kobo IS NULL) OR
           (topup_minimum_kobo > 0 AND topup_maximum_kobo >= topup_minimum_kobo));
