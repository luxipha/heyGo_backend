-- No existing fares, balances or ledger rows are changed. Publishing a rule
-- remains an explicit Admin action; this table starts empty.
CREATE TABLE rider_offers (
 id UUID PRIMARY KEY REFERENCES admin_campaign_drafts(id),
 title TEXT NOT NULL, message TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN('discount','gocoin_multiplier')),
 discount_kobo BIGINT NOT NULL DEFAULT 0 CHECK(discount_kobo BETWEEN 0 AND 100000000),
 multiplier INT NOT NULL DEFAULT 1 CHECK(multiplier BETWEEN 1 AND 10),
 code TEXT UNIQUE, starts_at TIMESTAMPTZ NOT NULL, ends_at TIMESTAMPTZ NOT NULL,
 market_codes TEXT[] NOT NULL, category_slugs TEXT[] NOT NULL,
 airport_only BOOLEAN NOT NULL DEFAULT false,
 start_hour INT NOT NULL DEFAULT 0 CHECK(start_hour BETWEEN 0 AND 23),
 end_hour INT NOT NULL DEFAULT 24 CHECK(end_hour BETWEEN 1 AND 24),
 timezone TEXT NOT NULL DEFAULT 'Africa/Lagos',
 budget_kobo BIGINT NOT NULL DEFAULT 0 CHECK(budget_kobo>=0),
 max_uses INT NOT NULL CHECK(max_uses BETWEEN 1 AND 1000000),
 per_rider_limit INT NOT NULL CHECK(per_rider_limit BETWEEN 1 AND 100),
 active BOOLEAN NOT NULL DEFAULT true,
 created_by UUID NOT NULL REFERENCES admin_users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK(ends_at>starts_at AND end_hour>start_hour),
 CHECK((kind='discount' AND discount_kobo>0 AND multiplier=1 AND budget_kobo>=discount_kobo) OR
       (kind='gocoin_multiplier' AND discount_kobo=0 AND multiplier>=2)),
 CHECK(cardinality(market_codes)>0 AND cardinality(category_slugs)>0)
);
CREATE TABLE rider_offer_claims (
 offer_id UUID NOT NULL REFERENCES rider_offers(id), rider_id UUID NOT NULL REFERENCES users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(offer_id,rider_id)
);
