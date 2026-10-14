-- Legacy fares may already have one or several trips. Recover the earliest
-- durable request and prevent an old quote from creating another trip.
UPDATE ride_fares f SET consumed_trip_id=t.id,consumed_at=t.created_at
FROM (SELECT DISTINCT ON (ride_fare_id) ride_fare_id,id,created_at
      FROM trips ORDER BY ride_fare_id,created_at,id) t
WHERE f.id=t.ride_fare_id AND f.consumed_at IS NULL;
