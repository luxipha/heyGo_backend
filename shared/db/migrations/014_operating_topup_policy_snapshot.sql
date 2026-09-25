ALTER TABLE driver_operating_topups ADD COLUMN IF NOT EXISTS market_code TEXT;
ALTER TABLE driver_operating_topups ADD COLUMN IF NOT EXISTS policy_id UUID REFERENCES operating_balance_policies(id);
