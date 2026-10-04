-- Synthetic Admin journey records. Run only against the disposable staging database.
-- Usage: PGOPTIONS='-c heygo.staging_seed=enabled' psql "$DATABASE_URL" -X -v ON_ERROR_STOP=1 -f scripts/seed-admin-staging.sql
-- Re-run is safe; reset by recreating the staging database/branch. Trip status events are append-only.
\set ON_ERROR_STOP on
BEGIN;

DO $guard$
BEGIN
  IF current_setting('heygo.staging_seed', true) IS DISTINCT FROM 'enabled' THEN
    RAISE EXCEPTION 'Set PGOPTIONS=-c heygo.staging_seed=enabled for the disposable staging database';
  END IF;
  IF EXISTS (
    SELECT 1 FROM users
    WHERE casper_id_user_id IS DISTINCT FROM 'heygo-admin-staging:rider'
      AND casper_id_user_id IS DISTINCT FROM 'heygo-admin-staging:partner-pending'
      AND casper_id_user_id IS DISTINCT FROM 'heygo-admin-staging:partner-history'
  ) THEN
    RAISE EXCEPTION 'Refusing to seed a database containing non-staging users';
  END IF;
END
$guard$;

INSERT INTO users (id, casper_id_user_id, human_id, roles, verified, phone_e164)
VALUES
  ('a1100000-0000-4000-8000-000000000001', 'heygo-admin-staging:rider', 'STAGE-RIDER-001', ARRAY['rider'], FALSE, NULL),
  ('a1100000-0000-4000-8000-000000000002', 'heygo-admin-staging:partner-pending', 'STAGE-DRIVER-001', ARRAY['driver'], FALSE, NULL),
  ('a1100000-0000-4000-8000-000000000003', 'heygo-admin-staging:partner-history', 'STAGE-DRIVER-002', ARRAY['driver'], FALSE, NULL)
ON CONFLICT (id) DO NOTHING;

INSERT INTO driver_profiles (driver_id, display_name, admin_status)
VALUES
  ('a1100000-0000-4000-8000-000000000002', 'Staging Partner Pending', 'pending'),
  ('a1100000-0000-4000-8000-000000000003', 'Staging Partner History', 'pending')
ON CONFLICT (driver_id) DO NOTHING;

INSERT INTO driver_vehicles (driver_id, package_slug, make, model, model_year, color, plate)
VALUES
  ('a1100000-0000-4000-8000-000000000002', 'sedan', 'Toyota', 'Corolla', 2022, 'White', 'STAGING-001'),
  ('a1100000-0000-4000-8000-000000000003', 'sedan', 'Toyota', 'Camry', 2021, 'Silver', 'STAGING-002')
ON CONFLICT (driver_id) DO NOTHING;

INSERT INTO drivers (id, name, car_plate, package_slug, status, available)
VALUES
  ('a1100000-0000-4000-8000-000000000002', 'Staging Partner Pending', 'STAGING-001', 'sedan', 'offline', FALSE),
  ('a1100000-0000-4000-8000-000000000003', 'Staging Partner History', 'STAGING-002', 'sedan', 'offline', FALSE)
ON CONFLICT (id) DO NOTHING;

INSERT INTO ride_fares (id, rider_id, package_slug, total_fare_minor, route, pickup, destination, created_at, expires_at)
VALUES
  ('a1100000-0000-4000-8000-000000000011', 'a1100000-0000-4000-8000-000000000001', 'sedan', 450000, '{"pickup":"Ikeja","destination":"Lekki","synthetic":true}'::jsonb, ST_SetSRID(ST_MakePoint(3.3415, 6.6018), 4326)::geography, ST_SetSRID(ST_MakePoint(3.4721, 6.4474), 4326)::geography, NOW(), NOW() + INTERVAL '15 minutes'),
  ('a1100000-0000-4000-8000-000000000012', 'a1100000-0000-4000-8000-000000000001', 'sedan', 520000, '{"pickup":"Yaba","destination":"Victoria Island","synthetic":true}'::jsonb, ST_SetSRID(ST_MakePoint(3.3792, 6.5095), 4326)::geography, ST_SetSRID(ST_MakePoint(3.4219, 6.4281), 4326)::geography, NOW() - INTERVAL '2 days', NOW() - INTERVAL '2 days' + INTERVAL '15 minutes')
ON CONFLICT (id) DO NOTHING;

INSERT INTO trips (id, rider_id, ride_fare_id, assigned_driver_id, status, market_code, created_at, updated_at, started_at, completed_at)
VALUES
  ('a1100000-0000-4000-8000-000000000021', 'a1100000-0000-4000-8000-000000000001', 'a1100000-0000-4000-8000-000000000011', NULL, 'pending', 'lagos', NOW(), NOW(), NULL, NULL),
  ('a1100000-0000-4000-8000-000000000022', 'a1100000-0000-4000-8000-000000000001', 'a1100000-0000-4000-8000-000000000012', 'a1100000-0000-4000-8000-000000000003', 'completed', 'lagos', NOW() - INTERVAL '2 days', NOW() - INTERVAL '2 days' + INTERVAL '1 hour', NOW() - INTERVAL '2 days' + INTERVAL '15 minutes', NOW() - INTERVAL '2 days' + INTERVAL '1 hour')
ON CONFLICT (id) DO NOTHING;

INSERT INTO support_cases (id, opened_by, audience, topic_code, trip_id, detail_code, subject, status, priority)
VALUES ('a1100000-0000-4000-8000-000000000031', 'a1100000-0000-4000-8000-000000000001', 'rider', 'fare', 'a1100000-0000-4000-8000-000000000022', '', 'Staging fare question', 'open', 'normal')
ON CONFLICT (id) DO NOTHING;

INSERT INTO support_case_messages (id, case_id, sender_kind, sender_user_id, sender_admin_id, client_message_id, body)
VALUES ('a1100000-0000-4000-8000-000000000032', 'a1100000-0000-4000-8000-000000000031', 'user', 'a1100000-0000-4000-8000-000000000001', NULL, 'a1100000-0000-4000-8000-000000000033', 'Synthetic staging case: please check the linked Trip fare.')
ON CONFLICT (id) DO NOTHING;

INSERT INTO trip_ratings (trip_id, actor_id, subject_id, rating, comment, admin_review_status)
VALUES ('a1100000-0000-4000-8000-000000000022', 'a1100000-0000-4000-8000-000000000001', 'a1100000-0000-4000-8000-000000000003', 1, 'Synthetic staging review for staff queue testing.', 'pending')
ON CONFLICT (trip_id, actor_id) DO NOTHING;

COMMIT;
