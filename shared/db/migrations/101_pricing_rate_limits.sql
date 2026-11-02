-- Rates are stored in kobo: 100,000 kobo = 1,000 naira.
ALTER TABLE pricing_package_rates DROP CONSTRAINT pricing_package_rates_per_km_kobo_check;
ALTER TABLE pricing_package_rates DROP CONSTRAINT pricing_package_rates_per_minute_kobo_check;
ALTER TABLE pricing_package_rates ADD CONSTRAINT pricing_package_rates_per_km_kobo_check CHECK (per_km_kobo BETWEEN 0 AND 100000);
ALTER TABLE pricing_package_rates ADD CONSTRAINT pricing_package_rates_per_minute_kobo_check CHECK (per_minute_kobo BETWEEN 0 AND 100000);
