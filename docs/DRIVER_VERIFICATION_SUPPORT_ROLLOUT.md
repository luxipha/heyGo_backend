# Driver verification and support changes — 21 October 2026

Implemented locally; not deployed or migrated on the production database.

- Migration 069 adds a singleton customer-support phone setting. GET/PUT /admin/settings/support-contact uses settings permission, CSRF on writes, international phone validation, optimistic version checks and audit reasons. Empty phone disables calling.
- GET /support/contact exposes only the configured business phone, with no-store caching. No staff or customer private contact data is returned.
- Admin Settings → Customer support reads and saves this setting. Driver verification support fetches the current phone before opening the system dialler and reports missing configuration or failures.
- Driver identification uses existing GET/PUT /driver/identity for manual review; CasperID is not required. Updating identity resets backend review status as designed. Existing identity fields are preserved.
- Inspection displays existing status and offers support/refresh. No Driver inspection-booking endpoint exists; arranging the inspection remains a support operation. This is not an automated booking flow.
- Operational document upload failures retain the drawer and captured images for retry. Duplicate taps during submission are ignored; camera permission errors are surfaced. Removed the sample licence image.
- Verification progress uses the backend requirement count and Home remains gated by backend eligibility.

Release order: apply migrations through 069 to the intended database, deploy API Gateway, deploy Admin, configure the CS number, rebuild Driver. Verify real document storage upload, Admin review, manual identity approval, inspection result and Driver eligibility refresh. These live steps and physical-device dialler/camera checks remain required. The isolated database integration test requires TEST_DATABASE_URL; never run destructive integration tests against production.
