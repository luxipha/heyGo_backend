-- Owner confirmation for a listed venue: the owner reads a short code, sent to their
-- phone (or email), to the Hero. Codes live with the other one-time codes so the same
-- per-destination send limits apply; this adds a purpose for them and a link to the
-- Hero who asked, and lets a listing record which confirmation it used (once only).
ALTER TABLE auth_otp_challenges DROP CONSTRAINT auth_otp_challenges_purpose_check;
ALTER TABLE auth_otp_challenges ADD CONSTRAINT auth_otp_challenges_purpose_check CHECK (purpose IN ('login','pin_reset','venue_owner'));
CREATE TABLE hero_owner_code_requests (
 challenge_id UUID PRIMARY KEY REFERENCES auth_otp_challenges(id) ON DELETE CASCADE,
 hero_id UUID NOT NULL REFERENCES heroes_accounts(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX hero_owner_code_requests_hero ON hero_owner_code_requests(hero_id,created_at DESC);
ALTER TABLE hero_venue_submissions ADD COLUMN owner_confirmation_id UUID UNIQUE REFERENCES auth_otp_challenges(id) ON DELETE SET NULL;
