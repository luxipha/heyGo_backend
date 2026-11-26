-- What a driver did with each scheduled ride they held. Feeds the driver
-- standing page; it does not affect matching.
--   checked_in       the driver confirmed readiness before dispatch
--   released         the driver gave the ride back (pickup_at kept so a
--                    "late release" threshold can be chosen later)
--   missed_check_in  dispatch came and the driver had not checked in
--   not_available    the driver checked in but was offline or busy at dispatch
-- Rider edits and cancellations are never recorded here.
CREATE TABLE driver_scheduled_outcomes (
    booking_id UUID NOT NULL REFERENCES scheduled_rides(id),
    driver_id UUID NOT NULL REFERENCES users(id),
    outcome TEXT NOT NULL CHECK (outcome IN ('checked_in','released','missed_check_in','not_available')),
    pickup_at TIMESTAMPTZ NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (booking_id, driver_id, outcome)
);
CREATE INDEX driver_scheduled_outcomes_driver_idx ON driver_scheduled_outcomes(driver_id, recorded_at DESC);
