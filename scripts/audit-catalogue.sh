#!/usr/bin/env bash
# Read-only catalogue audit: ride categories, vehicle catalogue, Driver
# vehicles/inspections, market pricing and service coverage.
#
# Usage:
#   scripts/audit-catalogue.sh --local            # DB used by the running local stack
#   scripts/audit-catalogue.sh --env-file FILE    # DATABASE_URL from FILE
#   DATABASE_URL=... scripts/audit-catalogue.sh   # explicit URL
#
# Safety: every statement runs in a READ ONLY transaction with
# default_transaction_read_only=on. Credentials are never printed; only the
# host, port, database name and server-side identifiers are shown.
# Production needs its own audit run before deployment; a local result says
# nothing about production.
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
url="${DATABASE_URL:-}"
source_label="DATABASE_URL environment variable"

case "${1:-}" in
  --local)
    pidfile="$ROOT/.dev/api-gateway.pid"
    [ -f "$pidfile" ] || { echo "error: local stack not running ($pidfile missing)" >&2; exit 1; }
    pid=$(tr -d ' \n' <"$pidfile")
    url=$(ps eww -p "$pid" -o command= 2>/dev/null | grep -oE "DATABASE_URL=[^ ]+" | head -1 | cut -d= -f2- | tr -d "'\"" || true)
    [ -n "$url" ] || { echo "error: could not read DATABASE_URL from api-gateway pid $pid" >&2; exit 1; }
    source_label="running local api-gateway (pid $pid)"
    ;;
  --env-file)
    file="${2:?--env-file needs a path}"
    url=$(grep -E '^DATABASE_URL=' "$file" | tail -1 | cut -d= -f2- | tr -d "'\"")
    source_label="$file"
    ;;
  -h|--help) sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  "") ;;
  *) echo "error: unknown option $1" >&2; exit 2 ;;
esac
[ -n "$url" ] || { echo "error: no DATABASE_URL (use --local, --env-file or DATABASE_URL)" >&2; exit 1; }

# host:port/db with user, password and query string removed.
redacted=$(printf '%s' "$url" | sed -E 's#^[a-z]+://([^@/]*@)?##; s#\?.*$##')

export PGOPTIONS='-c default_transaction_read_only=on'
export PGCONNECT_TIMEOUT=10
q() {
  printf '\n== %s\n' "$1"
  psql "$url" -X -q -v ON_ERROR_STOP=1 -P pager=off -P footer=off \
    -c "BEGIN READ ONLY;" -c "$2" -c "ROLLBACK;"
}

echo "Catalogue audit · $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "URL source : $source_label"
echo "Target     : $redacted"

q "Database identity" "
SELECT current_database() AS database,
       inet_server_addr() AS server_addr,
       inet_server_port() AS server_port,
       split_part(version(), ' on ', 1) AS server_version,
       (SELECT system_identifier FROM pg_control_system()) AS cluster_system_id,
       COALESCE((SELECT setting FROM pg_settings WHERE name='neon.timeline_id'), 'n/a (not Neon)') AS neon_timeline_branch_id,
       COALESCE((SELECT setting FROM pg_settings WHERE name='neon.tenant_id'), 'n/a') AS neon_tenant_project_id,
       pg_is_in_recovery() AS read_replica;"

q "Schema migrations" "
SELECT COUNT(*) AS applied, MAX(version) AS latest, MAX(applied_at) AS last_applied_at
FROM schema_migrations;"

files=$(cd "$ROOT/shared/db/migrations" && ls *.sql | sed "s/'/''/g; s/.*/('&')/" | paste -sd, -)
q "Repo migrations not applied" "
SELECT f.version AS not_applied
FROM (VALUES $files) AS f(version)
WHERE NOT EXISTS (SELECT 1 FROM schema_migrations m WHERE m.version=f.version)
ORDER BY 1;"

q "Ride categories (all)" "
SELECT slug, name, status, legacy, passengers, minimum_year, requires_ac,
       revision, approved_by IS NOT NULL AS approved, updated_at::date
FROM ride_categories ORDER BY status, slug;"

q "Catalogue models per category" "
SELECT c.slug, c.status, COUNT(m.model_id) AS linked_models,
       COUNT(m.model_id) FILTER (WHERE v.status='active') AS active_models
FROM ride_categories c
LEFT JOIN vehicle_category_models m ON m.category_id=c.id
LEFT JOIN vehicle_catalogue v ON v.id=m.model_id
GROUP BY c.slug, c.status ORDER BY 1;"

q "Vehicle catalogue" "
SELECT status, body_type, COUNT(*) AS models, MIN(year_from) AS earliest_year, MAX(COALESCE(year_to, year_from)) AS latest_year
FROM vehicle_catalogue GROUP BY 1, 2 ORDER BY 1, 2;"

q "Driver vehicles (inspected attributes)" "
SELECT package_slug, review_status, COUNT(*) AS vehicles,
       MIN(model_year) AS min_year, MAX(model_year) AS max_year,
       MIN(passenger_seats) AS min_seats, MAX(passenger_seats) AS max_seats,
       COUNT(*) FILTER (WHERE working_ac) AS working_ac,
       COUNT(*) FILTER (WHERE catalogue_model_id IS NULL) AS not_in_catalogue
FROM driver_vehicles GROUP BY 1, 2 ORDER BY 1, 2;"

q "Approved per-car category grants" "
SELECT c.slug, COUNT(*) AS drivers
FROM driver_vehicle_categories d JOIN ride_categories c ON c.id=d.category_id
GROUP BY 1 ORDER BY 1;"

q "Inspections" "
SELECT status, COUNT(*) AS inspections, MAX(inspected_at)::date AS latest
FROM driver_inspections GROUP BY 1 ORDER BY 1;"

q "Pricing versions" "
SELECT market_code, version, status, effective_from, effective_until,
       approved_by IS NOT NULL AS approved
FROM pricing_versions ORDER BY market_code, version;"

q "Live rates by market and category (approved, in effect)" "
SELECT v.market_code, r.package_slug, r.base_kobo, r.per_km_kobo, r.per_minute_kobo
FROM pricing_package_rates r JOIN pricing_versions v ON v.id=r.pricing_id
WHERE v.status='approved' AND v.effective_from<=NOW() AND (v.effective_until IS NULL OR v.effective_until>NOW()) ORDER BY 1, 2;"

q "Active categories without a live rate (per live market)" "
SELECT m.market_code, c.slug AS missing_rate
FROM (SELECT DISTINCT market_code FROM pricing_versions v WHERE v.status='approved' AND v.effective_from<=NOW() AND (v.effective_until IS NULL OR v.effective_until>NOW())) m
CROSS JOIN ride_categories c
WHERE c.status='active' AND NOT EXISTS (
  SELECT 1 FROM pricing_package_rates r JOIN pricing_versions v ON v.id=r.pricing_id
  WHERE v.status='approved' AND v.effective_from<=NOW() AND (v.effective_until IS NULL OR v.effective_until>NOW()) AND v.market_code=m.market_code AND r.package_slug=c.slug)
ORDER BY 1, 2;"

q "Zone rules on live pricing" "
SELECT v.market_code, z.kind, COUNT(*) AS rules
FROM pricing_zone_rules z JOIN pricing_versions v ON v.id=z.pricing_id
WHERE v.status='approved' AND v.effective_from<=NOW() AND (v.effective_until IS NULL OR v.effective_until>NOW()) GROUP BY 1, 2 ORDER BY 1, 2;"

q "Service coverage (geofences)" "
SELECT kind, code, name, version, status, effective_from, effective_until,
       approved_by IS NOT NULL AS approved
FROM trip_geofences ORDER BY kind, code, version;"

echo
echo "Audit complete (read-only; no changes made)."
