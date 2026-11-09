-- Offer quotes are separate, append-only records. Previewing an offer never
-- modifies the underlying fare, GoCoin promise or wallet.
CREATE TABLE rider_offer_quotes (
 fare_id UUID PRIMARY KEY REFERENCES ride_fares(id),
 discount_offer_id UUID REFERENCES rider_offers(id),
 coin_offer_id UUID REFERENCES rider_offers(id),
 discount_kobo BIGINT NOT NULL DEFAULT 0 CHECK(discount_kobo>=0),
 coins BIGINT NOT NULL DEFAULT 0 CHECK(coins>=0),
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE TRIGGER rider_offer_quote_immutable BEFORE UPDATE OR DELETE ON rider_offer_quotes FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
ALTER TABLE trips ADD COLUMN offer_discount_kobo BIGINT NOT NULL DEFAULT 0 CHECK(offer_discount_kobo>=0);
CREATE TABLE rider_offer_uses (
 trip_id UUID NOT NULL REFERENCES trips(id), offer_id UUID NOT NULL REFERENCES rider_offers(id),
 rider_id UUID NOT NULL REFERENCES users(id),
 discount_kobo BIGINT NOT NULL DEFAULT 0 CHECK(discount_kobo>=0),
 status TEXT NOT NULL DEFAULT 'reserved' CHECK(status IN('reserved','redeemed','released')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(trip_id,offer_id)
);
CREATE INDEX rider_offer_limits ON rider_offer_uses(offer_id,rider_id,status);
