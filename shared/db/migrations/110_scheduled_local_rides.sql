CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE TABLE scheduled_ride_policies(
 version BIGSERIAL PRIMARY KEY, enabled BOOLEAN NOT NULL DEFAULT false,
 market_codes TEXT[] NOT NULL DEFAULT '{}', category_slugs TEXT[] NOT NULL DEFAULT '{}',
 min_advance_minutes INT NOT NULL DEFAULT 60 CHECK(min_advance_minutes BETWEEN 60 AND 10080),
 max_advance_minutes INT NOT NULL DEFAULT 10080 CHECK(max_advance_minutes BETWEEN 60 AND 43200),
 dispatch_minutes INT NOT NULL DEFAULT 30 CHECK(dispatch_minutes BETWEEN 10 AND 60),
 readiness_minutes INT NOT NULL DEFAULT 45 CHECK(readiness_minutes BETWEEN 15 AND 120),
 created_by UUID REFERENCES admin_users(id),created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK(max_advance_minutes>=min_advance_minutes AND min_advance_minutes>readiness_minutes AND readiness_minutes>dispatch_minutes),
 CHECK(NOT enabled OR (cardinality(market_codes)>0 AND cardinality(category_slugs)>0))
);
INSERT INTO scheduled_ride_policies DEFAULT VALUES;
CREATE TRIGGER scheduled_policy_immutable BEFORE UPDATE OR DELETE ON scheduled_ride_policies FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
CREATE TABLE scheduled_rides(
 id UUID PRIMARY KEY,rider_id UUID NOT NULL REFERENCES users(id), request_id UUID NOT NULL,
 fare_id UUID NOT NULL UNIQUE REFERENCES ride_fares(id), service_type TEXT NOT NULL DEFAULT 'local' CHECK(service_type='local'),
 policy_version BIGINT NOT NULL REFERENCES scheduled_ride_policies(version),
 pickup_at TIMESTAMPTZ NOT NULL,dispatch_at TIMESTAMPTZ NOT NULL,readiness_at TIMESTAMPTZ NOT NULL,ends_at TIMESTAMPTZ NOT NULL,
 market_code TEXT NOT NULL,origin_region_code TEXT NOT NULL,destination_region_code TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'open' CHECK(status IN('open','reserved','dispatched','cancelled','unfulfilled','completed')),
 driver_id UUID REFERENCES users(id),ready_at TIMESTAMPTZ, reminder_sent_at TIMESTAMPTZ,
 trip_id UUID UNIQUE REFERENCES trips(id),version BIGINT NOT NULL DEFAULT 1,
 cancellation_actor TEXT CHECK(cancellation_actor IN('rider','system')),reason TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(rider_id,request_id),CHECK(readiness_at<dispatch_at AND dispatch_at<pickup_at AND pickup_at<ends_at),
 CHECK(status<>'reserved' OR driver_id IS NOT NULL),CHECK(status<>'dispatched' OR trip_id IS NOT NULL),
 EXCLUDE USING gist(rider_id WITH =,tstzrange(readiness_at,ends_at,'[)') WITH &&) WHERE(status IN('open','reserved','dispatched')),
 EXCLUDE USING gist(driver_id WITH =,tstzrange(readiness_at,ends_at,'[)') WITH &&) WHERE(driver_id IS NOT NULL AND status IN('reserved','dispatched'))
);
CREATE INDEX scheduled_rides_due ON scheduled_rides(dispatch_at,id) WHERE status IN('open','reserved');
CREATE TABLE scheduled_ride_declines(booking_id UUID NOT NULL REFERENCES scheduled_rides(id),driver_id UUID NOT NULL REFERENCES users(id),created_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(booking_id,driver_id));
CREATE TABLE scheduled_ride_notifications(booking_id UUID NOT NULL REFERENCES scheduled_rides(id),driver_id UUID NOT NULL REFERENCES users(id),PRIMARY KEY(booking_id,driver_id));
CREATE FUNCTION scheduled_offer_eligible(owner UUID,booking UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM scheduled_rides s JOIN users rider ON rider.id=s.rider_id JOIN ride_fares f ON f.id=s.fare_id JOIN drivers d ON d.id=owner JOIN users u ON u.id=d.id JOIN driver_eligibility e ON e.driver_id=d.id
 WHERE s.id=booking AND s.rider_id<>owner AND e.eligible_to_drive AND u.active AND 'driver'=ANY(u.roles) AND rider.active AND NOT rider.rider_trip_restricted AND 'rider'=ANY(rider.roles)
 AND driver_category_eligible(owner,f.package_slug) AND driver_balance_eligible(owner,s.market_code)
 AND d.online_market_code=s.market_code
 AND NOT EXISTS(SELECT 1 FROM simulation_participants WHERE actor_user_id IN(owner,s.rider_id))
 AND NOT EXISTS(SELECT 1 FROM driver_ride_preferences p WHERE p.driver_id=owner AND ((cardinality(p.ride_types)>0 AND NOT f.package_slug=ANY(p.ride_types)) OR (cardinality(p.working_areas)>0 AND NOT s.origin_region_code=ANY(p.working_areas))
 OR (NOT p.long_distance AND COALESCE((f.route->'routes'->0->>'distance')::numeric,1e20)>25000)
 OR (NOT p.airport AND EXISTS(SELECT 1 FROM trip_geofences g WHERE g.kind='airport' AND g.status='approved' AND g.effective_from<=NOW() AND (g.effective_until IS NULL OR g.effective_until>NOW()) AND (ST_Covers(g.boundary,f.pickup::geometry) OR ST_Covers(g.boundary,f.destination::geometry))))))
 AND NOT EXISTS(SELECT 1 FROM driver_home_destinations h WHERE h.driver_id=owner AND h.enabled AND (d.location IS NULL OR NOT ST_Distance(f.destination,h.location)<ST_Distance(d.location,h.location))))
$$;
-- A future commitment blocks ordinary rides that are likely to finish after
-- readiness, while permitting the scheduled booking's own dispatch offer.
CREATE FUNCTION driver_schedule_allows(owner UUID,active_trip UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM scheduled_rides s WHERE s.driver_id=owner AND s.status IN('reserved','dispatched') AND s.ends_at>NOW()
 AND (active_trip IS NULL OR s.trip_id IS DISTINCT FROM active_trip)
 AND s.readiness_at<=NOW()+CASE WHEN active_trip IS NULL THEN INTERVAL '0 seconds' ELSE
 make_interval(secs=>COALESCE((SELECT GREATEST(0,LEAST(86400,(f.route->'routes'->0->>'duration')::double precision))+900 FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.id=active_trip),86400)) END)
$$;
-- Synchronize terminal outcomes even if the maintenance process restarts.
CREATE FUNCTION sync_scheduled_trip() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.status IN('completed','cancelled') THEN
 UPDATE scheduled_rides SET status=CASE WHEN NEW.status='completed' THEN 'completed' ELSE 'unfulfilled' END,reason=CASE WHEN NEW.status='cancelled' THEN COALESCE(NEW.cancellation_reason,'trip_cancelled') ELSE reason END,version=version+1,updated_at=NOW() WHERE trip_id=NEW.id AND status='dispatched';
 ELSIF NEW.assigned_driver_id IS NOT NULL THEN
 UPDATE scheduled_rides SET driver_id=NEW.assigned_driver_id,version=version+1,updated_at=NOW() WHERE trip_id=NEW.id AND status='dispatched' AND driver_id IS DISTINCT FROM NEW.assigned_driver_id;
 END IF; RETURN NEW; END $$;
CREATE TRIGGER scheduled_trip_terminal AFTER UPDATE OF status,assigned_driver_id ON trips FOR EACH ROW EXECUTE FUNCTION sync_scheduled_trip();
ALTER TABLE trips ADD COLUMN preferred_driver_id UUID REFERENCES users(id);
