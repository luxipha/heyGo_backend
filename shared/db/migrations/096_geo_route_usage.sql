-- Provider-neutral counters only: no rider, driver, key, query or location data.
CREATE TABLE IF NOT EXISTS geo_route_daily_usage (
 day DATE NOT NULL,
 provider TEXT NOT NULL CHECK (provider IN ('google','amap','osrm')),
 attempts BIGINT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 successes BIGINT NOT NULL DEFAULT 0 CHECK (successes >= 0),
 failures BIGINT NOT NULL DEFAULT 0 CHECK (failures >= 0),
 first_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (day,provider),
 CHECK (successes + failures <= attempts)
);
