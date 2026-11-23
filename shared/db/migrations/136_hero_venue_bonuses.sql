-- Hero bonuses for a venue they listed: a go-live bonus and an orders bonus when it
-- reaches N delivered orders. Neither is queued until the venue's first delivery.
-- Amounts are admin settings; each bonus is a pending earning that staff approve,
-- exactly like an order share.
ALTER TABLE heroes_settings ADD COLUMN live_bonus_kobo BIGINT NOT NULL DEFAULT 50000 CHECK(live_bonus_kobo BETWEEN 0 AND 10000000);
ALTER TABLE heroes_settings ADD COLUMN orders_bonus_kobo BIGINT NOT NULL DEFAULT 50000 CHECK(orders_bonus_kobo BETWEEN 0 AND 10000000);
ALTER TABLE heroes_settings ADD COLUMN orders_bonus_count INT NOT NULL DEFAULT 5 CHECK(orders_bonus_count BETWEEN 1 AND 1000);
ALTER TABLE heroes_settings ADD COLUMN review_hours_min INT NOT NULL DEFAULT 5 CHECK(review_hours_min BETWEEN 0 AND 720);
ALTER TABLE heroes_settings ADD COLUMN review_hours_max INT NOT NULL DEFAULT 48 CHECK(review_hours_max BETWEEN 0 AND 720);
ALTER TABLE heroes_settings ADD CONSTRAINT heroes_review_hours_order CHECK(review_hours_min<=review_hours_max);

ALTER TABLE hero_venue_earnings ADD COLUMN kind TEXT NOT NULL DEFAULT 'order_share' CHECK(kind IN ('order_share','live_bonus','orders_bonus'));

ALTER TABLE heroes_wallet_entries DROP CONSTRAINT heroes_wallet_entries_kind_check;
ALTER TABLE heroes_wallet_entries ADD CONSTRAINT heroes_wallet_entries_kind_check CHECK(kind IN ('referral_reward','venue_order_share','venue_bonus'));
