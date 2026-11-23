-- Venues listed by Heroes. The venue itself lives in the venue platform; this
-- table keeps the Hero's own history and the reference needed for later
-- rewards. stage mirrors the venue's progress as reported by that platform.
CREATE TABLE hero_venue_submissions (
 id UUID PRIMARY KEY,
 hero_id UUID NOT NULL REFERENCES heroes_accounts(id),
 partner_submission_id TEXT NOT NULL UNIQUE,
 venue_name TEXT NOT NULL CHECK(length(venue_name) BETWEEN 1 AND 120),
 stage TEXT NOT NULL DEFAULT 'waiting_for_owner' CHECK(stage IN ('waiting_for_owner','owner_joined','in_review','needs_changes','live','archived')),
 note TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX hero_venue_submissions_hero ON hero_venue_submissions(hero_id,created_at DESC);
