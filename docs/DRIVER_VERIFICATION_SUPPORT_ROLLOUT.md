# Driver verification and support changes — 21 October 2026

Backend and Admin deployed on 21 October 2026. Driver changes remain in source and require a rebuilt app.

- Migration 069 adds a singleton customer-support phone setting. GET/PUT /admin/settings/support-contact uses settings permission, CSRF on writes, international phone validation, optimistic version checks and audit reasons. Empty phone disables calling.
- GET /support/contact exposes only the configured business phone, with no-store caching. No staff or customer private contact data is returned.
- Admin Settings → Customer support reads and saves this setting. Driver verification support fetches the current phone before opening the system dialler and reports missing configuration or failures.
- Driver identification uses existing GET/PUT /driver/identity for manual review; CasperID is not required. Updating identity resets backend review status as designed. Existing identity fields are preserved.
- Inspection displays existing status and offers support/refresh. No Driver inspection-booking endpoint exists; arranging the inspection remains a support operation. This is not an automated booking flow.
- Operational document upload failures retain the drawer and captured images for retry. Duplicate taps during submission are ignored; camera permission errors are surfaced. Removed the sample licence image.
- Verification progress uses the backend requirement count and Home remains gated by backend eligibility.

Release order: apply migrations through 069 to the intended database, deploy API Gateway, deploy Admin, configure the CS number, rebuild Driver. Verify real document storage upload, Admin review, manual identity approval, inspection result and Driver eligibility refresh. These live steps and physical-device dialler/camera checks remain required. The isolated database integration test requires TEST_DATABASE_URL; never run destructive integration tests against production.

## Deployment evidence — 21 October 2026

- Release PR: https://github.com/luxipha/heyGo_backend/pull/20
- Merge/image revision: 8c49d73118373cc5e55a7efe6893f961aa76a14c. Includes Go 1.26.10 security patch; PR and main quality/security checks passed. CI exercised database-backed integration tests.
- Migration execution heygo-migration-sdgfh completed successfully, applying migrations through 069.
- Production traffic: API Gateway api-gateway-00008-voy; Driver driver-service-00002-dej; Trip trip-service-00002-waj; Payment payment-service-00005-sug. Each serves 100 percent traffic. Existing service environment configuration was retained.
- Ride Agent has no deployed Cloud Run service and was not created as part of this release. The generic workflow assumes it exists; this rollout explicitly targeted the four existing services.
- API and Payment readiness passed. Candidate and production staff login/logout and support/geo/review-OTP reads passed. Email authentication enabled; SMS disabled; review OTP inactive.
- Saved the previously approved testing-week public OSRM choice via Admin settings (version 1); this is real route computation, not seeded trips or placeholder maps.
- Admin published to https://n.heygo.ng, document root /domains/n.heygo.ng/public_html. Live entrypoint references match the build; five static assets returned 200 with matching SHA-256 bytes. Cloudflare injects its own challenge script into HTML; asset references were verified separately. Avatar returned 200.
- CS phone deliberately remains empty. Admin staff enter their number in Settings → Customer support. No fake phone number was persisted.
- Physical-device camera/dialler tests, mobile rebuild, and the full document upload → Admin review → Driver eligibility journey remain required. No claim of full mobile production readiness.
