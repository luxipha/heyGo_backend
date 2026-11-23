-- A Hero's share of the platform fee on an order from a venue they listed. The
-- venue platform reports it once the rider has delivered the order; staff approve
-- it before it becomes a wallet credit (same approval rhythm as stage rewards).
CREATE TABLE hero_venue_earnings (
 id UUID PRIMARY KEY,
 hero_id UUID NOT NULL REFERENCES heroes_accounts(id),
 partner_order_id TEXT NOT NULL UNIQUE,
 venue_name TEXT NOT NULL DEFAULT '',
 amount_kobo BIGINT NOT NULL CHECK(amount_kobo>0 AND amount_kobo<=100000000),
 platform_fee_kobo BIGINT NOT NULL DEFAULT 0 CHECK(platform_fee_kobo>=0),
 delivered_at TIMESTAMPTZ,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','rejected')),
 reviewed_by UUID REFERENCES admin_users(id),
 reviewed_at TIMESTAMPTZ,
 reject_reason TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK(status='pending' OR reviewed_at IS NOT NULL)
);
CREATE INDEX hero_venue_earnings_pending ON hero_venue_earnings(created_at) WHERE status='pending';
CREATE INDEX hero_venue_earnings_hero ON hero_venue_earnings(hero_id,created_at DESC);

-- Approved earnings become immutable wallet entries, one per earning.
ALTER TABLE heroes_wallet_entries ADD COLUMN venue_earning_id UUID UNIQUE REFERENCES hero_venue_earnings(id);
ALTER TABLE heroes_wallet_entries DROP CONSTRAINT heroes_wallet_one_referral;
ALTER TABLE heroes_wallet_entries ADD CONSTRAINT heroes_wallet_one_referral CHECK(num_nonnulls(referral_id,programme_referral_id,stage_id,venue_earning_id)=1);
ALTER TABLE heroes_wallet_entries DROP CONSTRAINT heroes_wallet_entries_kind_check;
ALTER TABLE heroes_wallet_entries ADD CONSTRAINT heroes_wallet_entries_kind_check CHECK(kind IN ('referral_reward','venue_order_share'));
