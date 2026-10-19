-- Report content remains in the configured provider. These are ownership and
-- delivery references only; no descriptions, screenshots or comments are copied.
CREATE TABLE bug_report_links (
 id UUID PRIMARY KEY,
 owner_id UUID NOT NULL REFERENCES users(id),
 provider TEXT NOT NULL,
 external_id TEXT,
 request_id UUID NOT NULL,
 request_hash TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(owner_id, request_id),
 UNIQUE(provider, external_id)
);
CREATE INDEX bug_report_links_owner_page ON bug_report_links(owner_id,created_at DESC,id DESC);
CREATE INDEX bug_report_links_page ON bug_report_links(created_at DESC,id DESC);
