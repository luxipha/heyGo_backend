-- What a Hero sees when they open one of their venues: who the owner is, and when it
-- was claimed and went live (first seen by the Hero's venue list, which is how it is kept).
ALTER TABLE hero_venue_submissions ADD COLUMN owner_name TEXT CHECK(owner_name IS NULL OR length(owner_name)<=100);
ALTER TABLE hero_venue_submissions ADD COLUMN claimed_at TIMESTAMPTZ;
ALTER TABLE hero_venue_submissions ADD COLUMN live_at TIMESTAMPTZ;
