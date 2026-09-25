-- Persist rider-feedback tags and retain who cancelled a trip for accurate
-- driver history and receipts.
ALTER TABLE trip_ratings
    ADD COLUMN IF NOT EXISTS feedback_tags TEXT[] NOT NULL DEFAULT '{}'::TEXT[];
ALTER TABLE trip_ratings
    DROP CONSTRAINT IF EXISTS trip_ratings_feedback_tags_check;
ALTER TABLE trip_ratings
    ADD CONSTRAINT trip_ratings_feedback_tags_check
    CHECK (cardinality(feedback_tags) <= 3 AND feedback_tags <@ ARRAY['clean_and_tidy','easy_pickup','great_chat']::TEXT[]);

ALTER TABLE trips
    ADD COLUMN IF NOT EXISTS cancellation_actor_id UUID REFERENCES users(id);

CREATE INDEX IF NOT EXISTS driver_trip_history_idx
    ON trips(assigned_driver_id,COALESCE(completed_at,cancelled_at,updated_at,created_at) DESC,id DESC);
