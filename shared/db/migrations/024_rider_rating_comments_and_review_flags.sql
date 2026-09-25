-- Rider comments are attached only to rider-authored reviews of drivers.
-- One-star rider reviews enter the HeyGo staff review queue.
ALTER TABLE trip_ratings
    ADD COLUMN IF NOT EXISTS comment TEXT NOT NULL DEFAULT '';
ALTER TABLE trip_ratings
    ADD CONSTRAINT trip_ratings_comment_length_check
    CHECK (char_length(comment) <= 250);

ALTER TABLE trip_ratings
    ADD COLUMN IF NOT EXISTS admin_review_status TEXT NOT NULL DEFAULT 'not_required'
    CHECK (admin_review_status IN ('not_required','pending','reviewed'));
ALTER TABLE trip_ratings
    ADD COLUMN IF NOT EXISTS admin_reviewed_by UUID REFERENCES admin_users(id),
    ADD COLUMN IF NOT EXISTS admin_reviewed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS trip_ratings_admin_review_idx
    ON trip_ratings(admin_review_status, created_at DESC, trip_id DESC)
    WHERE admin_review_status IN ('pending','reviewed');
CREATE INDEX IF NOT EXISTS trip_ratings_driver_received_idx
    ON trip_ratings(subject_id, created_at DESC, trip_id DESC);
