-- Extend the existing append-only credit ledger's allowlist. No existing
-- entries or balances are modified; promotion credit still requires an
-- explicitly completed trip and a reserved campaign budget.
ALTER TABLE rider_credit_entries DROP CONSTRAINT rider_credit_entries_kind_check;
ALTER TABLE rider_credit_entries DROP CONSTRAINT rider_credit_entries_check;
ALTER TABLE rider_credit_entries ADD CHECK(kind IN('referral_reward','ride_payment','gocoin_redemption','promotion_reward'));
ALTER TABLE rider_credit_entries ADD CHECK(
 (kind IN('referral_reward','gocoin_redemption') AND delta_kobo>0 AND trip_id IS NULL) OR
 (kind='promotion_reward' AND delta_kobo>0 AND trip_id IS NOT NULL) OR
 (kind='ride_payment' AND delta_kobo<0 AND trip_id IS NOT NULL));
