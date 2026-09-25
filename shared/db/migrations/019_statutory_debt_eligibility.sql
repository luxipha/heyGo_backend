-- Only driver-funded Operating Balance charges are driver arrears.
CREATE OR REPLACE FUNCTION driver_balance_eligible(target_driver UUID, target_market TEXT) RETURNS BOOLEAN AS $$
    SELECT target_market IS NOT NULL AND COUNT(*) = 1
       AND COALESCE((SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=target_driver),0) >= MIN(p.minimum_kobo)
       AND COALESCE((SELECT SUM(c.amount_kobo-COALESCE(r.repaid_kobo,0))
                     FROM trip_statutory_charges c LEFT JOIN
                       (SELECT charge_id,SUM(amount_kobo) AS repaid_kobo FROM statutory_charge_repayments GROUP BY charge_id) r
                       ON r.charge_id=c.id WHERE c.driver_id=target_driver
                         AND c.bearer='driver' AND c.funding_source='operating_balance'),0)=0
    FROM operating_balance_policies p
    WHERE p.market_code=target_market AND p.status='approved'
      AND p.effective_from<=NOW() AND (p.effective_until IS NULL OR p.effective_until>NOW())
$$ LANGUAGE sql STABLE;
