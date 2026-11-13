# Geo integration rollout and verification

Status: source integration implemented; Google APIs/keys and the approved $25 monthly Maps alert are provisioned. Production application migrations/deployment and full device navigation/tracking tests remain pending.

## Scope

Driver uses a provider-neutral navigation adapter with the Google Navigation Flutter plugin pinned to 0.10.0, which supports the existing CocoaPods workflow. Newer 0.11 releases require a separate Swift Package Manager migration; do not update the plugin without testing native builds. Driver iOS minimum is now **16.0**, Android minimum **24**. This is a device-support change to account for in rollout.

Rider retains Google Maps Flutter for map display. Its mounted Home receives selected pickup/destination, confirmed route and fresh driver positions from existing backend snapshots/events. Long-press the live map to select a pickup pin when a new ride is allowed. Rider does not receive Driver turn-by-turn navigation. Onboarding illustrations and explicitly disabled debug maps remain presentation-only.

`shared/geo` owns normalized routing, OSRM mapping, validation, legacy polyline output and Admin configuration. Existing Trip fare APIs and Driver route responses retain their wire contracts. Prices remain frozen on fare quotes. PlaceService wraps existing explicit destination searches/resolution; **autocomplete is not introduced** by this change, so current search billing remains Text Search/Details as configured by fields.

## Admin configuration

1. Apply migration `067_geo_settings.sql` through the existing migration runner on the intended database (no startup migration assumption).
2. Deploy the API Gateway and Admin changes after review. Routes require `settings.manage`; writes require CSRF and create audit records.
3. Open Settings → Maps & navigation. Choose OSRM and enter the complete endpoint ending in `/route/v1/driving`.
4. For the user-authorized testing week, select `https://router.project-osrm.org/route/v1/driving` and enable **Allow public OSRM demo for testing**. Save with a change reason. No supported hosting is provisioned yet.
5. Enable Google mobile maps, Places and Driver navigation only after corresponding APIs, restricted keys and app builds are ready. Saving settings does not enable Google APIs or change billing.
6. Deploy the Trip service changes with the settings table and initial routing configuration already present. New quotes/routes read settings from the database. No paid fallback occurs.

Later replace the endpoint with a supported public HTTPS OSRM host and disable demo mode. No mobile rebuild is required for that endpoint change. Endpoints reject embedded credentials, query strings, nonstandard ports and local IP/metadata targets; DNS is rechecked at connection time, redirects/proxy use are disabled. Private/VPC-only endpoints are intentionally unsupported until a specific trusted transport is implemented.

Mobile switches refresh within a minute and on applicable resume paths. Failed first reads keep maps/navigation disabled; transient failures retain the last confirmed settings. A running navigation leg continues through a disable switch, then no next leg starts. This is configuration for supported adapters only: adding Huawei/AMap/HERE still requires implementation, licensing and validation. Native keys remain in protected platform build configuration; server Places keys remain in Secret Manager.

## Google provisioning (approved and applied)

The user approved Google API/key setup and a **$25 monthly alert**. `infra/geo` was applied in isolated `geo` state: Maps Android/iOS, Navigation, Places New, API Keys and Billing Budgets APIs; four app-restricted mobile testing keys; one Places-only server key; and 50/90/100% billing alerts scoped to Maps catalog services. A subsequent plan reported no changes. No application/database/OSRM infrastructure was deployed by this module.

Mobile keys are installed in ignored native config files with restricted local permissions. The Places key is stored as version 1 of the existing `heygo-ride-agent-places-key` secret. IAM was not changed. A real search and detail resolution for Ikeja City Mall passed through HeyGo's Google adapter. This exposed and fixed an existing body-on-GET bug; a regression test protects the fix. No end-to-end authenticated trip or live navigation success is claimed by that search test.

- Android IDs: `ng.heygo.partner` (Driver), `ng.heygo.rider` (Rider); bind each key to the exact signing certificate SHA-1. Current debug signing is not a Play-release signing setup.
- iOS IDs: `ng.heygo.partner` (Driver), `ng.heygo.rider` (Rider); separate bundle-restricted keys.
- Required services: Maps SDK Android/iOS, `navigationsdk.googleapis.com`, Places API New. The provisioning tool also needs API Keys API.
- Copy iOS keys directly into ignored `ios/Flutter/Secrets.xcconfig` as `GOOGLE_MAPS_API_KEY = ...`; Android also reads the ignored per-app `android/geo.properties`, or a local Gradle property/environment value with the same name. Never commit or print keys.
- Store the server key in the existing `heygo-ride-agent-places-key` secret and verify Ride Agent deployment/routing separately. Restrict it to Places API; fixed-egress IP restriction is not configured.
- Billing alerts are not spending caps. Set deliberate per-API quotas after inspecting supported metrics; confirm alerts are scoped using verified Cloud Billing service IDs.

For Rider debug builds, enable the native map with `--dart-define=GOOGLE_MAPS_ENABLED=true`; release defaults to real maps. `DEV_PLACEHOLDER_MAP=true` keeps an explicit preview. Driver's existing preview flags still work and do not prove live navigation or backend tracking.

## Validation gates

- Contract tests: OSRM coordinate order, invalid routes/geometry, cancellation, endpoint configuration and known polyline output.
- Admin: persistence, stale-version conflict, unsafe endpoint rejection, settings permission mapping, audit writes and actual API-client CSRF behavior.
- Navigation: destination deduplication across rebuilds, pickup-to-destination transition, obsolete queued target suppression, stop/dispose behavior. Fake-adapter tests do not prove Google delivery.
- Rider: preview and stored-trip geometry mapping; reject invalid coordinate data. Existing suites must remain green.
- Real Android/iOS: Google terms accepted/declined; denied or disabled GPS; poor accuracy; network loss; rerouting; app background/foreground; lock/unlock; notification resume; stationary driver; socket reconnect; pickup arrival; trip start/completion/cancellation; sign-out. Confirm trip controls remain tappable and backend events remain authoritative.
- Verify captured GPS timestamps/heading/speed arrive at the backend. Tracking uses a foreground notification on Android, automotive background location configuration on iOS, and a fresh-fix heartbeat while online. Process-kill reliability and actual background delivery remain device-test gates.
- Confirm no repeated `setDestinations` calls on widget rebuilds and inspect actual Navigation billing counts. Validate API key rejection for a wrong certificate/bundle.
- Run the temporary public OSRM option only as agreed for testing. A successful demo response is not a hosted service SLA.

## Outstanding launch prerequisites outside this change

Driver real email/phone authentication, cancellation integrity, payment/top-up recovery, session isolation, signing/store requirements and the rest of the screen audit remain separately tracked. This geo work does not establish production readiness of the entire app.

## Recorded validation

- Driver: 61 tests passed; analyzer clean; iOS simulator native build succeeded.
- Rider: 91 tests passed; iOS simulator native build succeeded. Later Classic map reuse is covered by final analysis/regression checks.
- Admin: TypeScript check and production build passed; two geo API-client tests passed.
- Backend: focused geo/Trip/Ride Agent/API tests passed; geo settings persistence/concurrency/audit/security tests also passed in a newly created isolated localhost test database, which was removed afterward.
- Terraform: provider-schema validation passed; approved API/key/budget resources applied; final plan reported no changes.
- Actual Google Places adapter: live search and selected-place resolution passed.
- Android builds and seeded active-trip visual check: see final execution report; not asserted complete in this snapshot.
- Live Google navigation/rerouting, physical-device background tracking and complete authenticated trips: pending.

## Rider optional push build configuration

The pre-existing Android Firebase file belongs to `everyone-2de50` with legacy package IDs, not `ng.heygo.rider`. Firebase is used only by Rider's opt-in push bridge, not maps, account login, chat storage or the HeyGo database. Android now applies the Google Services plugin only when the same `--dart-define=HEYGO_RIDER_PUSH_ENABLED=true` flag enables runtime push. With push disabled, map/testing builds need no Firebase project configuration. Enabling push requires a current matching Firebase client file; the build fails clearly instead of registering the wrong app. Push delivery remains unverified/unconfigured until that separate setup is completed. This does not remove Firebase or change in-app socket messaging.

## Android package migration — 21 October 2026

Driver Android applicationId/namespace: ng.heygo.partner. Rider Android applicationId/namespace: ng.heygo.rider. Kotlin MainActivity declarations and directories match these namespaces. iOS app/test bundle IDs now match the same application identities. Existing OAuth callback schemes remain independent of application IDs.

The existing Android geo testing keys now allow the new package IDs with their existing debug signing certificate. Their API restrictions are preserved. Play signing still requires the final Play app-signing certificate restrictions. Rider's legacy Firebase file has not been rewritten or substituted; push stays opt-in and requires a matching ng.heygo.rider Firebase Android client.

Universal application identities are now ng.heygo.partner (Driver) and ng.heygo.rider (Rider) on both Android and iOS. Both iOS geo keys were updated to these bundle IDs while retaining their API restrictions. Apple distribution provisioning and any Firebase registrations must use these final identities; simulator builds do not establish App Store signing readiness.
