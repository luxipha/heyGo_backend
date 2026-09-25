# Driver trip geofences

Trip classification is decided by the backend when the rider creates a trip. The fare preview saves the exact requested pickup and destination separately from the road-snapped OSRM route.

## Admin boundary workflow

All routes require a HeyGo staff session. Mutations also require `X-CSRF-Token`.

- `POST /admin/geofences` creates a draft with `kind` (`market`, `region`, or `airport`), uppercase `code`, `name`, GeoJSON `boundary` (Polygon or MultiPolygon), `effectiveFrom`, and optional `effectiveUntil`. The backend assigns the next version for each kind/code.
- `GET /admin/geofences?kind=&status=&cursor=` lists up to 50 versions with a `meta.nextCursor`. `GET /admin/geofences/:id` returns the boundary and approval details.
- `POST /admin/geofences/:id/approve` requires a different staff account from the creator. Approval rejects a boundary whose interior overlaps an approved zone of the same kind for any overlapping effective period. Adjacent zones may share an edge; a pickup exactly on a shared edge is treated as ambiguous and trip creation fails.
- `POST /admin/geofences/:id/retirement-requests` submits `{ "effectiveUntil": "...UTC..." }`. A second staff account approves it at `POST /admin/geofences/:id/retirement-requests/:requestID/approve`.

Every change records its actor and details in `admin_audit`. Approved boundary geometry, name, code, and version cannot be changed or deleted. Replacing a zone means retiring the old version and approving a new version with a non-overlapping effective interval.

## Classification

At trip creation, pickup must match exactly one approved market and one approved region. Destination must match exactly one approved region. Airport matches are optional at either endpoint, but multiple airport matches at one point are ambiguous. Approved versions must be effective when the trip is created.

The trip records `market_code`, `origin_region_code`, `destination_region_code`, `regulatory_tags`, `classified_at`, and a JSON snapshot of the matched geofence IDs, codes, and versions. Different origin/destination regions give `INTERSTATE`; matching regions give `LOCAL`. Airport matches add `AIRPORT_PICKUP` or `AIRPORT_DROPOFF`. Vehicle package does not participate in this classification.

If a required match is missing or any match is ambiguous, trip creation returns `trip_market_unavailable` (HTTP 409). No geofences are seeded by migrations; Admin must publish the launch market and jurisdiction boundaries before trip creation can work. Static Go tests pass, but the PostGIS integration test requires `TEST_DATABASE_URL` and has not run in this workspace.
