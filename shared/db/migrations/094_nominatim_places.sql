-- Persistent result cache and a shared upstream budget for explicit place searches.
CREATE TABLE IF NOT EXISTS geo_place_cache (
 cache_key TEXT PRIMARY KEY,
 results JSONB NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS geo_place_cache_expiry ON geo_place_cache(expires_at);
CREATE TABLE IF NOT EXISTS geo_place_upstreams (
 upstream TEXT PRIMARY KEY,
 next_request_at TIMESTAMPTZ NOT NULL
);
