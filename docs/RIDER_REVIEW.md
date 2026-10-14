# Rider backend completion task list

Source audit: 2026-10-12. Scope: `backend` and the mounted Agent-first `apps/rider` implementation.

Task status is recorded below. J-01/J-02 and J-03 source work and controlled automated verification are complete. J-04–J-18 source work is described separately below; preserved old UI follow-ups and intended-environment provider/device verification remain open. Existing endpoints elsewhere are reuse candidates, not completed Rider integrations. Mark completion separately as **Source**, **Automated**, **Staging**, and **Provider/live**; an endpoint or passing local test does not prove operational readiness.

## J-01–J-02: Authentication and enrollment

- [x] Integrate existing `POST /auth/register` with `{role: "rider"}` after authentication; verify the role before entering Rider-protected journeys. New OTP accounts currently have empty roles.
- [x] Test first-time phone/email sign-in, returning accounts, role enrollment retries, and session restore.
- [x] Clear credentials and return to onboarding when refresh is invalid/revoked; preserve appropriate recovery for temporary network failures.
- [x] Serialize concurrent refresh attempts and share the rotated token pair across waiting requests.
- [x] Retry authenticated requests safely; preserve mutation idempotency identities across refresh/retry.
- [ ] Verify OTP delivery, configured gateway access, refresh rotation, backend logout/revocation, and local credential removal in the intended environment.

### J-01/J-02 implementation and verification evidence — 2026-10-12

| Evidence level | Status | Evidence |
| --- | --- | --- |
| Source | Complete | Rider role is verified after OTP and on restore before Home; missing roles use existing `/auth/register`. Root routing owns Home/onboarding and dismisses nested routes on session invalidation. |
| Automated — Rider | Passed | 26 Flutter tests across `test/rider_session_test.dart` and existing `test/widget_test.dart`; focused analysis of changed source/tests reports no issues. |
| Automated — backend/local gateway | Passed | Focused auth tests against an isolated PostgreSQL 17/PostGIS database and real loopback gateway HTTP router. Both phone/email first-time and returning sessions, role gating/enrollment retry, refresh rotation, old-token rejection, consumed OTP rejection and logout/revocation passed. |
| Staging | Not verified | No deployed staging gateway or seeded staging account was exercised. |
| Provider/live and installed device | Not verified | OTP delivery adapters were recorded in memory; no SMS carrier/Resend inbox delivery or installed-device Keychain/Keystore behavior was observed. |

#### Implemented behavior

- [x] Parse `/auth/me` roles and require `rider` for signed-in routing; re-read identity after registration and refuse Home if enrollment is unconfirmed.
- [x] Resume failed enrollment on the saved session without consuming its OTP again; changing contact/channel does not reuse another pending identity.
- [x] Preserve credentials on offline/timeout/503 restore failure and show a recovery action; invalid/revoked refresh or deactivated accounts clear identity and credentials and return to onboarding.
- [x] Share concurrent refresh attempts; a late 401 from the old access token uses the already-rotated session. Replay occurs once at most.
- [x] Persist access/refresh tokens and expiry metadata together in one secure-storage record; migrate legacy credential keys on rotation and delete both formats on logout/invalidation.
- [x] Serialize storage writes/deletion and reject stale in-flight operations so a refresh cannot resurrect a signed-out session.
- [x] Require explicit `replaySafe` for automatic authenticated replay. Support create/reply capture one client message UUID outside the retry closure and reuse it as both `clientMessageId` and `Idempotency-Key`.
- [x] Parse the backend nested error envelope. Unrelated Support pagination/read-payload work remains under J-22.
- [x] Return backend 503 for a failed refresh database lookup; reserve 401 for missing/invalid/revoked refresh tokens.
- [x] Include the active Rider service in normal analyzer coverage.

#### Test identities and environment used

The user authorized synthetic test identities. The environment was an isolated local test target, not a deployed/provider environment.

- Current phone fixture: `+2348012345678`, synthetic Nigerian-format input intercepted by the test adapter; no real SMS was sent. This is not claimed to be an unassigned/reserved number. Rider phone entry defaults to `+234`; local Nigerian numbers are normalized by the backend. The earlier J-01/J-02 run used a UK fictional number; the current regression uses `+234`.
- Email in the earlier J-01/J-02 recorded gateway run: `rider-auth-97348ad5-9261-403b-91ad-78577e1e5d1e@example.com`. Backend tests generate a fresh `rider-auth-<UUID>@example.com`; no real email was sent.
- Flutter fake-session email: `rider-auth@example.com`; fake OTP/challenge values exist only in controlled tests and are not an app fallback.
- Database: `heygo_rider_auth_test` on `127.0.0.1:55439`, temporary PostgreSQL cluster under `/private/tmp/heygo-rider-auth-pg`; the existing `.env.local` database was not used.
- Earlier J-01/J-02 recorded gateway URLs: `http://127.0.0.1:63200` for phone and `http://127.0.0.1:63203` for email. These were ephemeral `httptest` servers, closed at test completion; they are not persistent app base URLs.
- J-03 regression recorded phone gateway `http://127.0.0.1:49156` and email gateway `http://127.0.0.1:49158`, with synthetic email `rider-auth-4ecdadba-2592-418d-a719-e312474cb175@example.com`. Both servers are closed; no real OTP delivery occurred.
- OTP adapters recorded destination/code in test memory. Tokens and OTPs were not copied into this document or fixtures for a deployed environment.
- The temporary PostgreSQL server is stopped after verification. Local credential removal was verified with the secure-storage test implementation, not a physical device.

#### Reproduction

From Rider:

```sh
flutter test test/rider_session_test.dart test/widget_test.dart
flutter analyze lib/main.dart lib/services/rider_api.dart lib/features/account/account_controller.dart lib/features/onboarding/onboarding_flow.dart test/rider_session_test.dart
```

From backend, with an isolated migrated PostGIS test database supplied through `TEST_DATABASE_URL`:

```sh
go test ./services/api-gateway/handler -run 'Test(RiderAuthEnrollmentRotationAndRevocation|RefreshDatabaseFailureIsTemporary|EmailOTPLoginCreatesVerifiedAccountAndSession|HTTPSMSSenderContract|NormalizePhone|NormalizeEmail|OTPHashBindsChallengePhoneAndCode|OTPHashBindsChannel)$' -count=1
```

#### Remaining gate for the sixth task

- [ ] Configure the intended deployed gateway in the Rider build through `HEYGO_API_URL` and exercise both new/returning accounts there.
- [ ] Observe real phone/email OTP delivery using controlled reachable recipients and the configured providers.
- [ ] Observe refresh rotation and backend logout/revocation through the installed Rider app against that target.
- [ ] Confirm Keychain/Keystore removal and signed-out state after app restart on the intended devices.

The sixth task stays unchecked until these observations exist. Local adapter delivery, PostgreSQL tests and widget routing do not imply provider/device completion.

## J-03: Home, active-trip recovery and availability

- [x] Implement a Rider-owned active-trip recovery read and trip-detail snapshot; define the no-active-trip response.
- [x] Implement nearby-driver availability/count reads using the same service-market, eligibility, presence, freshness and package rules as matching.
- [x] Hydrate Home identity from authoritative Rider identity and replace hard-coded nearby counts/greetings with real or unavailable states.
- [x] Restore an active trip on launch/resume and prevent Home from initiating a conflicting request.

### Implemented contracts and behavior

| Contract | Result |
| --- | --- |
| `GET /rider/trips/active` | Session-authenticated and Rider-role protected. `200 {"data":{"trip":null}}` means no recoverable trip. Otherwise `data.trip` contains the persisted snapshot. Database failure is `503 trip_unavailable`, never a fabricated empty state. |
| `GET /rider/trips/:tripID` | Rider-owned snapshot; malformed UUID returns `400 invalid_trip_id`; absent/another Rider's trip returns `404 trip_not_found`. |
| `GET /rider/availability?latitude=<lat>&longitude=<lng>&packageSlug=<slug>` | Aggregate count, package, market, 15,000 m radius, `observedAt` and location freshness of 1,800 seconds. Missing/invalid coordinates or unavailable package return 400; absent/overlapping active market returns `409 market_unavailable`; database errors return `503 availability_unavailable`. No nearby-driver IDs or precise locations are disclosed. |
| `POST /trip/start` conflict | Trip-service creation locks the Rider's user row and checks the same recovery predicate before insert. A competing creation returns gRPC `AlreadyExists`, mapped to HTTP `409 active_trip_exists`. |

Snapshots include ID, durable status/version/timestamps, requested pickup/destination coordinates, stored route/package, NGN fare, approved no-show debt/amount due, cancellation reason, and direct-settlement status. Accepted-driver fields contain only ID/name/photo/plate and a timestamped location if fresh. Pending/offered assignments disclose no driver details. Private Driver earnings, identity/contact data and regulatory snapshots are excluded.

Recovery covers `pending`, `assigned`, `accepted`, `arrived`, `started`, and completed trips with pending/disputed direct settlement. Confirmed completed trips and cancelled trips are terminal for recovery. If legacy data contains multiple recoverable trips, the read returns the newest by creation time/ID; creation remains blocked until none remain. Direct-settlement confirmation is a Driver assertion, not bank/provider payment proof.

Availability and dispatch now share `shared/driverstate/availability.go`: available status, available/online-requested flags, approved eligibility, unexpired socket presence, matching operating market and actual location market, location recorded within 30 minutes, eligible operating balance, exact package, and pickup radius. Dispatch keeps its trip-specific previous-offer exclusion, trust ordering and reservation locks; an aggregate availability count does not promise an assignment or ETA. Home explicitly requests the `sedan` package and labels the count as sedan drivers.

Mounted changes: `apps/rider/lib/main.dart` passes the authenticated session into `features/home/home_page.dart`; `home_controller.dart` reads recovery and availability through `services/rider_api.dart`. Greeting and initials use `/auth/me` identity, with a neutral fallback. Device location uses permission-aware `geolocator` reads; there is no hard-coded Lekki coordinate or count. Location denial/timeouts, market failures and network errors show unavailable state. The count expires after 60 seconds and can be refreshed from its Home pill.

On launch/resume, request controls stay blocked until the active-trip read succeeds. A recovered trip appears in the existing Home surface with a readable label for its actual status, Driver/fare/amount-due information and Refresh action. Raw IDs and coordinates stay in the read contract rather than appearing as product copy. It is not advanced by the preview timers. New destination, matching, new-ride and Classic entry actions check the recovery gate; matching rechecks immediately before proceeding. Late responses from an earlier recovery cannot overwrite a resumed trip. Pausing invalidates the gate and cancels preview timers. A backend creation guard also protects against requests from different devices.

`geolocator` is pinned to `14.0.2` because newer versions conflict with the existing secure-storage Windows dependency. The installed SDK resolved the lockfile and generated plugin registrations. Existing Android/iOS location permissions were retained. J-04–J-12 source work now adds Ride Agent/place resolution, confirmed trip creation and Rider WebSocket recovery; remaining preserved-UI/provider/device work is recorded in those sections.

### Verification and remaining environment checks

- [x] Local PostGIS-backed reads: explicit no-active response; owned detail/404 ownership; pending through started recovery; pending settlement recovery and confirmed settlement clearing; offered-driver nondisclosure; invalid coordinates/package and outside-market errors.
- [x] Availability exclusions: expired socket presence, stale location, wrong package, inactive/ineligible account, unavailable/online flags, wrong market, outside radius and insufficient operating balance. Fixture writes use a real transaction and roll back.
- [x] Real gateway/session middleware: unenrolled Rider recovery is 403; enrolled phone/email accounts can read recovery. Nigerian-format synthetic phone input is recorded only in the test OTP adapter.
- [x] Concurrent repository creation: an existing trip conflicts; two simultaneous new requests produce exactly one success and one `ErrActiveRiderTrip`; classification and persisted fare/event behavior remain tested.
- [x] Backend regression tests passed for the changed shared, Driver service, Trip service and gateway packages; separate database-backed cases above use the local isolated target.
- [x] Focused Flutter analysis of Home, session, root routing and tests passed with no issues; both repository whitespace checks passed.
- [x] Flutter Home/session/onboarding tests: 35 tests cover identity/counts, unavailable states, malformed HTTP snapshots, recovery races, mounted restoration, request blocking, resume and count expiry.
- [ ] Verify deployed gateway access, real market/pricing/balance configuration and live Driver presence against the intended Rider build.
- [ ] Verify device location permission/denial, background/resume and process restart on Nigerian target devices; inspect the mounted UI visually. Widget tests do not prove installed-device/native-plugin behavior.

Reproduce with `flutter test --no-pub test/rider_home_test.dart test/rider_session_test.dart test/widget_test.dart` from Rider. From backend with an isolated migrated `TEST_DATABASE_URL`, run gateway tests matching `TestRiderHomeOwnershipRecoveryAndAvailability|TestRiderAuthEnrollmentRotationAndRevocation|TestRefreshDatabaseFailureIsTemporary`, then `go test ./services/trip-service/repo -run TestPostgresTripRepositoryPersistsFareAndTrip -count=1`. Source and local automated completion do not imply deployed/provider/device completion.

## J-04–J-08: Ride Agent, places, routes and preview

- [x] Implement the separate Ride Agent Gateway and versioned structured response schema.
- [x] Implement place search/resolution, stable place identifiers, clarification candidates, and confirmed pickup/destination metadata.
- [x] Define a server-side LLM feature flag and deterministic fallback; validate tool requests and model output.
- [x] Restrict Agent tools to approved ride operations; exclude account, wallet, privacy, notification and Admin operations and sensitive telemetry.
- [x] Require explicit Rider confirmation for trip creation, cancellation and payment-adjacent actions.
- [x] Integrate existing `POST /trip/preview` route geometry/distance/duration and approved market pricing into the mounted route/options flow.
- [x] Define option availability and pickup ETA semantics; never infer dispatch availability from a returned price.
- [x] Expose actual fare `expiresAt` and define preview identity/grouping semantics; carry backend-issued fare IDs through confirmation.
- [x] Standardize public monetary fields as integer Kobo with explicit NGN currency; handle the legacy floating-point `totalFareInPaise` contract deliberately.
- [x] Return distinguishable expired/invalid/foreign fare, Rider restriction, market/service-unavailable and changed-debt errors.
- [x] Handle expired previews, route edits, unavailable pricing/service and changed debt by obtaining a new authoritative preview.
- [x] Integrate existing `POST /trip/start` with the selected valid fare and stable idempotency key.
- [x] Enforce one active trip per Rider atomically, including simultaneous requests with different idempotency keys.
- [x] Enforce single-use fare consumption atomically; define retry/recovery responses for an already-created trip.

### Scope and evidence for J-04–J-08

Checked tasks describe implemented source behavior, not deployed/provider/device readiness. The mounted Agent flow uses a new consistent backend card for resolution, preview and explicit booking; the preserved old map/Classic widgets remain separate deferred UI work. Validated route geometry is retained with backend distance/duration; the old schematic is not represented as the returned route. Cancellation confirmation is enforced at the backend, while its old UI integration remains J-13. Agent payment/account tools are excluded.

See [RIDER_RIDE_AGENT.md](RIDER_RIDE_AGENT.md) for versioned payloads, errors, quote grouping/single-use rules, service configuration and all preserved-UI/release follow-ups. New migrations: 054 (quotes/deadlines/debt serialization/metadata) and 055 (legacy consumed-fare recovery).

- [ ] Configure and verify real Nigerian place-provider results, deployed service authentication and approved content-retention/attribution policy.
- [ ] Connect actual geometry to the preserved old map/route inspector without changing its approved layout; implement later with the user.
- [ ] Connect the preserved Classic flow to backend contracts; it remains a separate preview implementation.

## J-09–J-12: Matching, assignment and live trip

- [x] Integrate existing Rider WebSocket and backend lifecycle events; replace local matching/lifecycle timers.
- [x] Persist a matching deadline and authoritative no-driver/timeout outcome; define retry/new-request behavior. No-driver and deadline outcomes now commit terminal state and durable events together.
- [x] Define a Rider snapshot-to-stream handoff that cannot lose events between snapshot retrieval and subscription.
- [x] Persist the event cursor and implement reconnect/replay, duplicate suppression and ordering. Rider connections without a cursor now replay from `evt_0`.
- [x] Implement Rider-owned assignment/driver-safe profile/vehicle/location/ETA reads and recovery after app restart.
- [x] Extend assignment payloads with the required vehicle description, assignment identity and pickup ETA/unavailable state.
- [x] Associate location updates with trip/assignment identifiers and include capture time/freshness semantics.
- [x] Correct location forwarding for `arrived` trips; verify accepted/arrived/started behavior and stale or former-assignment updates.
- [x] Implement authoritative arrival/start/completion/rematching/cancellation states and remove local driver fixtures from live presentation.

### Scope and evidence for J-09–J-12

The mounted server-backed recovery surface uses durable snapshots/events and safe assignment/vehicle/location fields. Existing driver chat/lifecycle/moving-map UI components retain their layouts and fixture implementations for later integration; those fixtures are not used by the new server-backed live surface. Pickup ETA remains explicitly unavailable until an authoritative estimator is implemented. Location events distinguish device capture from the legacy server-receipt fallback.

- [ ] Integrate the preserved old driver chat, lifecycle cards and moving map with real trip/assignment IDs, respecting their existing UI; retain J-13 chat/cancellation work.
- [ ] Observe real deployed WebSocket reconnect/replay and installed-device background/process recovery.
- [ ] Verify production push-mode deadline handling under delayed/retried broker and Cloud Tasks delivery.

### J-04–J-12 verification evidence — 2026-10-12

| Evidence level | Status | Evidence |
| --- | --- | --- |
| Source | Implemented in the new mounted backend flow | Separate destination gateway, validated versioned places/preview schema, approved server pricing, explicit start confirmation, atomic fare/Rider guards, persisted matching outcomes, owned recovery and Rider event stream. Preserved old UI follow-ups remain unchecked above. |
| Automated — Rider | Passed | Combined **44 Flutter tests** across Home/session/onboarding, ride controller and trip stream; focused analysis of the final mounted source and new tests reports no issues. New tests cover no booking before confirmation, same key after failed response, edit/expiry/debt reconfirmation, restart and terminal-trip recovery, integer money parsing, and real loopback WebSocket replay/duplicate suppression. |
| Automated — backend | Passed | Relevant gateway, Trip/Driver service, Ride Agent, shared messaging and state package regressions. Provider/LLM adapters use controlled fakes or loopback HTTP servers; no real provider call is claimed. |
| Automated — local PostGIS | Passed | Single-use retry after fare expiry; different-fare concurrency; expired quote rejection; grouped published-market prices/actual expiration; durable no-driver/timeout and actual deadline-worker closure; accepted/arrived/started scoped location events and former-assignment exclusion; event inserted between tail/snapshot reads remains replayable; confirmation/error/geometry contracts and legacy error/idempotency/auth regressions. |
| Staging / Provider / native device | Not verified | Real Google Places, configured planner, private deployed Agent service, production broker/task deadlines, installed GPS/secure storage/process restart/background sockets remain release gates. |

The isolated target remains `heygo_rider_auth_test` on `127.0.0.1:55439`. All phone OTP regression uses synthetic `+2348012345678` through the recording test adapter; no SMS is sent. Place fixtures use Lagos coordinates and generic test IDs; neither fake places nor prices are installed as production fallbacks. Temporary servers are closed and the local PostgreSQL cluster is stopped after the verification run. No deployment, commit or push was performed.

From Rider:

```sh
flutter test --no-pub --concurrency=1 test/rider_home_test.dart test/rider_session_test.dart test/widget_test.dart test/rider_ride_test.dart test/rider_trip_stream_test.dart
```

From backend, with the isolated migrated `TEST_DATABASE_URL`:

```sh
go test -p 1 ./services/trip-service/repo ./services/trip-service/service -run 'TestPostgresTripRepositoryPersistsFareAndTrip|TestQuoteUsesPublishedMarketPricingAndPersistsVersion' -count=1
go test -p 1 ./services/driver-service/repo -run 'TestMatchingTerminalOutcomeIsDurable|TestMatchingNoDriverAndDeadlineWorkerClosePendingTrip' -count=1
go test -p 1 ./services/api-gateway/handler -run 'TestRiderHomeOwnershipRecoveryAndAvailability|TestRiderMutationsRequireConfirmation|TestRiderQuoteErrorsRemainDistinguishable|TestRiderPreviewMapsAuthoritativeGeometry|TestRiderTripStartUsesErrorEnvelopeAndIdempotency|TestRiderAuthEnrollmentRotationAndRevocation|TestRefreshDatabaseFailureIsTemporary' -count=1
go test ./services/ride-agent-gateway/agent -count=1
```

## J-13–J-15: Chat, cancellation, issues and safety

- [x] Integrate existing participant-scoped trip message history/send/read endpoints using real trip IDs, message IDs and stable client-message IDs.
- [x] Handle chat pagination, delivery/read events, active-trip send restrictions and the 30-day post-trip read window.
- [x] Integrate existing cancellation with explicit reason and Rider confirmation; preserve server state restrictions and never create an automatic fee.
- [x] Define Rider command acknowledgment/result reads or events with confirmed, rejected and unresolved outcomes. HTTP 202 currently means queued, not applied.
- [x] Implement recovery when a cancellation response/event is lost or the trip state changes before the queued command applies.
- [x] Integrate existing trip-linked issue reporting and SOS with real trip IDs and participant authorization.
- [x] Preserve SOS request idempotency and recent location validation; show intake acknowledgment accurately.
- [x] Carry trip/case context into Support and back to the originating journey.
- [x] Expose approved no-show claim/debt status to Rider where required; retain configured wait, Driver claim and Finance review before debt creation.

## J-16–J-18: Settlement, receipts, ratings and history

- [x] Implement Rider-owned trip settlement/payment-status recovery reads.
- [x] Integrate pending/confirmed/disputed direct-payment settlement events and reads; a Driver assertion is not provider-verified payment success.
- [x] Implement Rider-owned paginated history and trip-detail reads with authoritative lifecycle, cancellation and settlement state.
- [x] Implement completed-trip receipts with a backend reference, fare/debt/currency breakdown and accurate settlement status.
- [x] Define rating eligibility and submitted-rating reads/acknowledgments; distinguish queued submission from persisted rating.
- [x] Integrate existing `POST /rider/trips/:tripID/rating`, optional comments and completed-trip participant rules; preserve one-star review behavior.
- [x] Replace local history/receipt insertion, fake booking references, payment-success markers and rating fixtures.
- [x] Rebook using authoritative pickup/destination metadata and a fresh preview; never reuse an old fare.
- [x] Connect post-trip issue reporting to the real completed trip and Support case.

### J-13–J-18 source implementation and verification

Checked tasks cover the mounted backend surfaces. Existing Agent driver-thread, Classic, legacy history and receipt widget layouts are preserved. The canonical flow now opens `backend_trip_pages.dart` from the recovered trip and Activity entry; it uses `trip_journey_controller.dart`, the authenticated session and the existing Rider stream. Old completed-history seed entries and local completion insertion are removed. Legacy visual widgets are retained for later integration rather than presented as authoritative live data.

Implemented Rider contracts:

| Contract | Behavior |
| --- | --- |
| `GET /rider/trips?limit=&cursor=` | Owned terminal history, stable timestamp/UUID cursor, persisted snapshots, explicit unavailable errors. |
| `GET /rider/trips/:tripID/settlement` | Pending/confirmed/disputed or unavailable; `confirmationSource:driver_entered`, `providerVerified:false`, integer Kobo/NGN. |
| `GET /rider/trips/:tripID/receipt` | Completed trips only; server reference `trip:<UUID>`, fare/debt/amount-due breakdown and actual settlement. No payment-success fiction. |
| `GET /rider/trips/:tripID/rating` | Eligibility and actual submitted rating/comment/review state; one-star reviews retain pending staff review. |
| `POST /rider/trips/:tripID/cancel` and existing Rider `POST .../rating` | Explicit confirmation, stable `clientCommandId` and `Idempotency-Key`; durable command and delivery outbox. HTTP 202 reports queued/unresolved. |
| `GET /rider/trips/:tripID/commands/:commandID` | Owned current result: queued/unresolved, confirmed or rejected with safe reason. Consumer records results and a durable `trip.event.command_result`; broker replay recovers a mutation committed before its result was recorded. |
| Existing `/trips/:tripID/messages`, send and read | Participant-scoped current conversation; real message/recipient/client IDs, pagination cursor, server persistence/read events, active sends and 30-day post-trip reads. Former Driver conversations cannot be read by a newly assigned Driver. |
| Existing `/trips/:tripID/issues`, owned `GET /rider/trips/:tripID/support-cases` | Real trip/case linkage and stable request identity; repeated confirmed input recovers one case, changed input conflicts. Linked Support reads send a real `throughMessageId` and return to the same trip page. |
| Existing `/trips/:tripID/sos`, owned `GET /rider/trips/:tripID/sos/:requestID` | Stable intake identity, recent coordinates for a new intake, recoverable original intake after its timestamp becomes stale or trip ends. Concurrent copies serialize; cross-trip identity reuse conflicts. Intake receipt does not promise emergency dispatch. |
| `GET /rider/trips/:tripID/no-show` | Safe owned claim/debt status; Driver request is separate from Finance-approved debt. Cancellation never creates automatic debt. |
| Version 1 `/trip/preview` with optional `sourceTripId` | Rebook resolves the stable destination again and derives previous pickup from an owned ended trip; a new preview/fare/confirmation is always required. Legacy trips without stable place metadata ask the Rider to choose a destination. |

Migrations 056–057 persist Rider command results and Support-case request identities. Queue intake and outbox commit together. Gateway queue/read requests attempt bounded outbox delivery; existing pull publishers and push-mode drain/sweeper infrastructure must be enabled in deployment. Applied/rejected results are persisted with their own event; a temporary consumer/storage failure retains queued/unresolved state for replay. Cancellation state restrictions and existing Finance-approved no-show wait/claim/debt/funding rules remain authoritative. Completed and cancelled snapshots do not expose current Driver GPS.

Verification:

- [x] Combined Rider regression: **56 tests passed**; subsequent focused runs passed after the last case-recovery changes. Focused analysis reports no issues.
- [x] Real loopback wire tests preserve chat pagination metadata, integer receipt validation, trip-linked Support references and actual read-through message IDs.
- [x] Local PostGIS tests cover owned/foreign receipts, pending/disputed/confirmed settlement assertions, queued versus confirmed cancellation, rejected cancellation after a start race, duplicate suppression, one-star staff review and one linked issue case per request identity.
- [x] Chat regression covers current participants, read receipts, post-trip send rejection, 30-day expiry and former-conversation exclusion after reassignment.
- [x] SOS regression covers recorded-intake replay despite a stale original timestamp, new stale-intake rejection and existing location-retention rules.
- [x] Relevant gateway, Trip service and shared messaging regressions passed; no-show cancellation/funding rules remain tested. Source whitespace checks pass.

All fixtures use the isolated local PostGIS target and controlled adapters. Synthetic phone input remains `+2348012345678` through test adapters; no SMS is sent. No real SOS, payment, provider call, deployment, commit or push is performed. The isolated cluster is stopped after verification.

Reproduce from Rider:

```sh
flutter test --no-pub --concurrency=1 test/rider_trip_journey_test.dart test/rider_trip_wire_test.dart test/rider_home_test.dart test/rider_session_test.dart test/widget_test.dart test/rider_ride_test.dart test/rider_trip_stream_test.dart
```

With an isolated migrated `TEST_DATABASE_URL`, backend:

```sh
go test -p 1 ./services/api-gateway/handler -run 'TestRiderHistoryReceiptRatingAndCommandRecovery|TestTripChatLifecycleAndParticipants|TestSOSIntakeLocationRetention|TestRiderHomeOwnershipRecoveryAndAvailability|TestRiderCancellation|TestNoShowDebtTransfers' -count=1
```


Remaining gates / preserved UI tasks:

- [x] Integrate the preserved old driver-thread, lifecycle, receipt and Classic visual widgets with these contracts without changing their approved layouts. Current canonical backend pages use the existing Rider tokens.
- [ ] Inspect new surfaces on installed Android/iOS devices, including dialog transitions, secure-storage restart recovery, background/resume and live event delivery.
- [ ] Verify deployed Pub/Sub delivery/outbox sweeper and recovery after service/broker interruptions; unobserved outcomes must remain unresolved.
- [ ] Verify actual operational SOS/Support intake and staff handling against a controlled staging target; intake acknowledgment is not responder dispatch.
- [ ] Resolve OSM plus Google provider selection/retention/attribution later, as requested. Rebook place resolution remains honestly unavailable until the chosen providers are configured.
- [x] Complete generic Support pagination/reply UI under J-22 and Profile metrics/trust under J-19; these are separate from trip-linked context handoff.

## J-19: Profile, metrics, trust and feedback

- [x] Implement Rider completed-trip metrics and received Driver feedback read models.
- [x] Integrate available `GET /auth/me` consumer trust snapshot, including status/calculation time/policy version and unavailable states.
- [x] Define and implement consumer snapshot history reads; do not introduce a separate app-specific score without a product policy.
- [x] Remove client-derived trust score and fixture feedback/counts from authoritative presentation.

### Implementation and verification: 2026-10-12

Completion above is **source/local-test completion**, not deployed or physical-device verification.

| Contract | Behavior |
|---|---|
| `GET /rider/profile/metrics` | Full persisted completed-trip count and received assigned-Driver rating count, independent of paginated history. |
| `GET /rider/profile/feedback?limit=&cursor=` | Completed-trip participant Driver ratings and approved existing feedback tags; stable timestamp/trip cursor. No Driver identity, raw comments or moderation records are disclosed. |
| `GET /auth/me` | Existing consumer score/status/calculation time/policy version. No local score formula or inferred “Excellent” status; missing score is unavailable. |
| `GET /rider/profile/trust-history?limit=&cursor=` | Recorded consumer snapshot changes only. Explicit `source: consumer_snapshot`, `completeCalculationHistory: false`, provider calculation time and server recording time. It is not CasperID's calculation ledger. |

Migration `058_consumer_trust_snapshots.sql` backfills the current available snapshot and records future changes to the already-disclosed consumer fields. Identical updates do not duplicate a snapshot. Clearing the consumer score clears retained snapshots; deleting the user cascades. Existing shared trust calculation metadata is preserved as supplied, not recalculated by Rider. No separate HeyGo Rider score is created.

Profile retains its existing metric cards and feedback card/drawer layout. Counts come from metrics, feedback is real or empty/unavailable, and the trust drawer displays the consumer snapshot plus its metadata. A consistent new history page reads recorded snapshots. Account presentation state clears on identity changes; pending Support replies use owner/case-scoped secure-storage identities.

The mounted trip detail/chat/receipt paths now reuse existing lifecycle cards, Driver header/context/message bubbles/composer and receipt components. Values are supplied by owned trip snapshots, real messages/read events, persisted ratings and receipts. Completion does not claim a selected/queued rating was persisted. Receipt uses the backend reference, actual settlement wording and real clipboard copy; issue actions retain the real trip context. Classic uses the shared authoritative Ride controller, actual package/fare IDs, clarification candidates, fresh previews and explicit confirmation. Existing illustrations remain presentation assets; this work does not establish live map/provider verification or screenshot parity.

Generic Support now consumes case/message cursors, supports earlier-message pagination and replies, orders messages chronologically, marks through actual message IDs and preserves a lost reply identity across controller/restart recovery. It does not treat a network failure as a delivered message. Existing case tiles and conversation bubbles are reused; the added reply/pagination controls use Rider components.

Verification: the isolated PostGIS suite passed Rider profile/history ownership, participant feedback filtering, trust snapshot recording/deduplication/clearing, lifecycle recovery, chat and SOS replay. Separate local tests passed Support user/staff workflow, Finance handoff, safety intake and stable outbox event identity after an acknowledged publication followed by failed commit. These are controlled local tests, not operational staff response or deployed broker/sweeper evidence.

Final full Rider regression run: **62 tests passed**, including preserved widget values, one-star selection without false persistence acknowledgment, Profile/feedback pagination, and secure-storage recovery of lost Support create/reply responses. Focused analysis passed. Native iOS simulator build succeeded and the initial onboarding screen was installed/launched and visually inspected on iPhone 17 / iOS 26.0. No physical Android/iOS devices were found. Authenticated Profile/trip surfaces, dialog transitions, real secure-storage restart, background/resume and live delivery remain device gates.

### Disposable staging created as configuration

See [RIDER_STAGING.md](RIDER_STAGING.md), [staging Compose](../scripts/rider-staging.compose.yaml) and [credential preparation](../scripts/prepare-rider-staging.py). Private local-only credentials were generated in ignored `.dev/rider-staging.env` (0600); no secrets are copied here. The configured gateway is `http://127.0.0.1:8087`, database port 55440 and emulator port 8086. **The Compose stack is not running or deployed**: Docker is unavailable here. A separate actual local database `heygo_rider_staging` was created on the existing isolated loopback cluster at port 55439, migrated and seeded. Staff account `rider-staging-support` has only Support/Safety roles; its random password is stored privately in `RIDER_STAGING_STAFF_PASSWORD`, not in this document. The seed refuses unrelated identities and uses the existing staff password implementation. GCP has an active login, but the `heygo-ng` Cloud Run service query returned no services. No real SOS intake, operational staff session or cloud interruption was performed. The local PostgreSQL cluster remains running for the created staging database; stop instructions are in the staging document. The document contains provisioning/verification steps and acceptance evidence to collect.

OSM plus Google selection, storage/retention and attribution remain deferred as requested. No OSM adapter or new Google activation is implied by these UI bindings; legacy unresolvable rebooking remains honestly unavailable.

Additional preserved-UI gate:

- [x] User approved “Refresh trip status” and “Check payment status” for the old “End trip” / “Payment sent” actions. Defaults and canonical bindings use these labels; actions still refresh authoritative state and layouts are preserved.
- [ ] Compare authenticated old/new widgets on installed devices at approved viewports, including Classic/Driver map illustrations versus authoritative geometry. Record any necessary old-UI changes as tasks before redesigning those visuals.


## J-20: Rider identity and account corrections

- [x] Implement initial, optional display-name capture in Personal details, persisted in Rider-owned `rider_profiles`; `/auth/me` exposes `riderDisplayName` from that source for Riders, including dual-role accounts; generic Driver name behavior is preserved.
- [x] Define approved staff-reviewed legal-name correction through owned Support cases and private evidence; self-managed display names and case acknowledgment do not verify legal identity.
- [x] Implement authenticated +234 phone changes requiring current/new OTP proofs, account ownership, collision/tombstone checks and atomic account-wide session revocation.
- [x] Integrate Rider-owned name/contact mutations and owned Support entry; do not update Firebase/local identity or reuse Driver/Admin mutations.
- [x] Preserve identity read/logout and Driver-only account behavior. Rider email reassignment through legacy `/auth/account` requires the new verification/linking flow.
- [x] Implement account-bound email verification/linking without automatic account merging; re-read persisted verification state. Saving an email alone is not verification.

Policy, routes, errors, retention and evidence boundaries: [RIDER_IDENTITY_MONEY_POLICY.md](RIDER_IDENTITY_MONEY_POLICY.md). Automated evidence: full analyzer clean, 82 Rider tests passing, database-backed gateway/payment suites passing, and final controlled HTTP staging check passing; iOS simulator build succeeds. See [verification evidence](RIDER_VERIFICATION_EVIDENCE.md). Phone changes without access to a verified current phone require Support; no alternative proof rule was invented. Provider delivery, staff/provider legal-identity completion and installed-device comparison remain separate live gates below.

## J-21: Payment and wallet scope

- [x] Confirm launch scope against `ADMIN_LAUNCH_MONEY_RULES.md` and explicit user approval: ordinary fares are paid directly to the Driver; provider-collected Finance-approved no-show debts remain supported.
- [x] Keep direct-payment assertions, provider-verified debt payment and no-show debt as distinct authoritative states.
- [x] Integrate owned no-show debt reads, explicit-confirmation checkout and payment status/history/recovery in the Rider payment journey.
- [x] Enforce verified email in gateway and payment service, preserve server/provider verification and stable persisted idempotency keys across retries/restart. Provider creation errors remain unresolved/pending instead of falsely failed.
- [x] Resolve wallet/card scope: instruments, generic wallet/ledger/balance/activity are **deferred by user approval**; no implementation claim is made for these capabilities. Preserved card fields cannot collect data while unavailable.
- [x] Resolve ordinary-trip checkout scope: HeyGo collection is **deferred by user approval**; no ordinary-trip lifecycle producer or checkout flow is enabled.
- [x] Keep card/provider secrets and payment creation server-owned; never represent debt as wallet balance or browser return as payment success.
- [x] Define trip-linked Support dispute handling; direct Driver fares are not HeyGo-refundable collections, and paid no-show-debt refunds remain outside first launch under the existing governed money rules.

Remaining J-20/J-21 provider/device gates:

- [ ] Observe real SMS/email delivery and installed-device contact-change/re-login recovery.
- [ ] Observe controlled provider no-show checkout, browser return/resume, webhook verification and unresolved-creation reconciliation; automated fake-provider recovery is not live payment proof.
- [ ] Observe actual staff legal-name review/private evidence and any authoritative provider correction; intake is not legal-identity verification.
- [ ] Review preserved wallet/card explanatory wording and authenticated old/new/map visuals before any further old-UI changes.

## J-22: Generic Support integration corrections

- [x] Retain backend message IDs in Rider DTOs and send required `throughMessageId` when marking a case read.
- [x] Fix conversation loading/reloading so the current bodyless read request cannot fail the entire `loadCase` flow.
- [x] Consume case/message pagination cursors and preserve unread state.
- [x] Parse backend errors from `{error: {code, message}}` rather than top-level fields.
- [x] Preserve client-message/idempotency identities across uncertain create/reply attempts.
- [ ] Verify topics, case creation, list/detail, conversation, replies, read state and staff updates end-to-end.
- [x] Keep attachments deferred unless an approved control requires them; then integrate existing private upload/ownership contracts.

## J-23: Saved places, privacy, safety and accessibility

- [x] Implement Rider-owned saved-place CRUD with stable place metadata and persisted reads.
- [x] Implement Rider privacy export request/status/delivery and account deletion request/status contracts with approved retention/review policy.
- [x] Implement Rider trusted-contact CRUD and persisted safety preferences.
- [x] Define and implement persisted accessibility preferences and their approved ride handoff.
- [x] Integrate controls with loading/error/unavailable states; never present in-memory changes as persisted account data.
- [x] Reuse shared backend primitives where appropriate while retaining Rider ownership and authorization; Driver routes remain Driver-only.

Implemented source contracts and local verification (2026-10-12):

- Migration `059_rider_account_management.sql`; handlers `rider_account.go` and `rider_privacy.go`. All `/rider/*` routes require Rider ownership; Driver account routes retain Driver authorization.
- Saved places: `GET /rider/saved-places`, `PUT/DELETE /rider/saved-places/:label`, `GET /rider/saved-places/:label/resolve`. Store stable provider/place IDs with optimistic versions; resolve current metadata through the configured Ride Agent before saving or using a destination. Limit 20 labels. Provider unavailability returns an explicit error. OSM/Google selection, retention and attribution remain deferred; no provider address/coordinates are cached in this table.
- Contacts: `GET /rider/trusted-contacts`, `PUT/DELETE /rider/trusted-contacts/:contactID`; stable UUIDs, limit five, Nigerian local phone normalization to `+234` and E.164 validation.
- Preferences: `GET/PATCH /rider/preferences`; persisted `reduceMotion` and `shareRideStatus`. Reduce Motion applies to Rider presentation on launch/resume, route transitions and Agent/Home animations; no medical/accessibility data or service guarantee is handed to Drivers. Automatic contact delivery remains unavailable (`rideSharingEffective: false`); storing a preference does not send a message.
- Privacy: `POST/GET /rider/data-export`, `GET /rider/data-export/:exportID`, `POST/GET /rider/account-deletion`. Explicit confirmation and stable `clientRequestId` are required. Status reads support recovery; pending means queued. Export requires a verified email, private storage and configured encrypted Resend settings; API responses never expose private archive links.
- Rider exports use a separate owned queue and the existing private ZIP/email worker, retry identities, encrypted delivery links, seven-day link expiry and cleanup. The archive includes Rider identity, saved places, contacts/preferences, consumer trust snapshots, owned trips/settlement, participant chat, submitted ratings, approved received feedback, no-show debts, Rider Support messages/attachments and SOS intake status. This is the implemented Rider archive scope; raw telemetry, other users' identities and private staff notes are excluded.
- Deletion reuses existing staff review/deactivation/anonymization, identity tombstones, session revocation and retained trip/financial records. User approved account-wide review for dual-role accounts. Review checks both roles' active trips and Rider pending/disputed settlement; it removes saved places/contacts/preferences, cancels exports and queues private-file cleanup. New trip creation checks the locked active Rider account to prevent a stale queued request from recreating a trip after deletion.
- Mounted account drawers use backend loading, failure, unavailable and persisted states; mutations wait for acknowledgment and authoritative reread. Existing layouts/tokens remain, with matching controls for contact CRUD and saved-place selection. Home's saved destination resolves through the backend instead of a fixture.
- Local PostgreSQL tests verify persistence, ownership, idempotent requests, provider failure, private export/expiry and reviewed deletion. Shared Driver export/deletion regressions pass. All 62 existing Flutter tests pass, plus three new account settings tests covering failed writes, lost rereads and malformed records; focused Flutter analysis passes.

Remaining release gates:

- [ ] Configure the chosen OSM/Google providers and verify live saved-place search/resolution, retention and attribution.
- [ ] Verify actual private storage/email delivery and deployed privacy-job scheduling, retries, expiry and cleanup. Local tests use controlled storage/email adapters.
- [ ] Inspect account controls, persisted accessibility and restart/background recovery on installed Android/iOS devices; static/widget checks do not prove layout parity on devices.
- [ ] Verify staff account-wide review and private-file cleanup against controlled staging. Existing Admin review contracts retain the legacy route names; document any required Admin wording/UI adjustment as a separate preserved-UI task.
- [ ] Define any additional accessibility ride requirement or actual trusted-contact delivery policy before adding a Driver handoff or sending notifications.

## J-24: Notifications and delivery

- [x] Implement Rider notification preferences and persist/read their state.
- [x] Implement Rider device registration/token rotation/removal with platform and account ownership.
- [x] Implement Rider inbox, unread counts, individual/bulk read actions and pagination.
- [x] Implement Rider push fan-out and delivery/retry records; existing notification/device tables and APIs target Drivers.
- [x] Define typed Rider deep-link targets for trips, receipts, Support, debt and other approved journeys.
- [x] Re-read authoritative state after a notification/deep link before exposing actions; handle missing/stale targets and signed-out entry.
- [ ] Verify real push delivery and preference behavior in the intended environment; a WebSocket event is not push-delivery proof.


Source and local verification completed 2026-10-12. See [RIDER_NOTIFICATION_DELIVERY.md](RIDER_NOTIFICATION_DELIVERY.md) for endpoints, versioned targets, preference semantics, session/token ownership, leases/retries and activation gates. Migration 060 is applied to the isolated test and seeded staging databases. Rider inbox/devices/preferences remain separate from Driver data and routes. Incoming taps re-read owned inbox and resource state; they never execute an action automatically.

Existing preference rows retain their layout/labels. New Inbox uses Rider tokens. Transparent Material wrapping and deferred first-frame loads fix rendering/navigation assertions without redesigning the old UI. Native Rider sockets now send a configured `HEYGO_WS_ORIGIN` matching gateway `ALLOWED_ORIGINS`; credential query values are removed before logging/tracing. Gateway and Terraform subscriptions include the created-trip event.

Android debug build verification failed on the existing Gradle 7.6.3 / Java 21 incompatibility; see the evidence document. Its automatic migrator changes were reverted. Provider/device gate remains open: current Android Firebase configuration does not match `com.heygo.rider`; iOS Firebase plist is absent; no controlled sender/APNs/device delivery was configured or observed. Server/build activation flags remain disabled by default. Provider acceptance, WebSocket replay and inbox persistence are distinct from actual device delivery.

## Verification and completion gates

- [x] Record Source, Automated, Staging and Provider/live evidence separately for every journey and its downstream handoff.
- [x] Run focused backend contract/domain tests for ownership, errors, expiry, role enrollment, duplicate trips/fares, terminal matching and command results.
- [x] Run database-backed tests for atomicity, concurrent requests, settlement/no-show/rating persistence, privacy and notification ownership.
- [x] Run Rider unit/widget tests for DTOs, refresh concurrency/failure, pagination, message IDs, unavailable states and recovery.
- [ ] Verify the mounted Agent UI and approved navigation visually; compilation alone does not prove visual fidelity.
- [ ] Complete authenticated seeded staging journeys, including app restart, reconnect, expired preview, no drivers, cancellation, completion, rating and Support.
- [ ] Verify intended-environment OTP, routing/place provider, push and any approved payment provider behavior independently.
- [x] Keep sensitive identity/route/support/financial data and credentials out of public/partner payloads and Agent telemetry unless explicitly required.

### Original audit verification — before J-01–J-03 implementation

- Focused route-registration, auth-normalization, rating-validation and payment-session tests passed.
- Other package tests passed with database integration tests disabled; this is not database behavior proof.
- The broader run was blocked by sandbox restrictions on local HTTP/WebSocket test listeners.
- Database integration execution, staging and provider/live verification remain outstanding.

Evidence is recorded per journey and downstream handoff in [RIDER_VERIFICATION_EVIDENCE.md](RIDER_VERIFICATION_EVIDENCE.md). Final Rider regression run: **72 tests passed** and focused analysis passed. Final broad backend JSON run: **202 test/subtest passes**, zero failures; only the intentional socket performance test is skipped. Functional local Pub/Sub replay ran with the real emulator, including an offline consumer and deduplication of lifecycle plus inbox events. Full authenticated seeded device/Trip-service/provider journeys remain unchecked.

Native iOS simulator build/install/initial Agent onboarding visual inspection passed. Canonical Notifications and Inbox were visually inspected in controlled 430×932 widget renders and their actual app navigation/theme. These checks do not close authenticated device screen parity. The staging HTTP command used real Rider/limited staff session middleware, session refresh, persisted reads, staff reply → inbox/read → Rider WebSocket replay; its OTP sender was intercepted and device token synthetic. No SMS, push, routing/payment provider or emergency responder outcome was inferred.

J-20 display-name/Support-review/current-and-new-phone verification rules and J-21 launch scope were approved on 2026-10-12 and implemented as described above. Wallet/cards and ordinary-fare collection are explicitly deferred. Provider/device/staff evidence remains open; no live verification is inferred from source or automated checks.

Next decision review:

1. Rider Firebase project/client registrations, APNs/sender credentials, signing and controlled physical-device target; Android Java/Gradle build compatibility is a separate concrete engineering gate.
2. OSM plus Google responsibilities for search/routing, fallback, stable IDs, retention and attribution (previously deferred).
3. J-20/J-21 real contact delivery, legal-review/provider handling and installed recovery evidence under the approved policy.
4. Future wallet/instrument/collection/refund product decisions, only if the explicitly deferred scope is reopened; direct Driver settlement remains distinct.
5. Private export delivery/staff cleanup and any actual trusted-contact/accessibility handoff policy; current persisted preferences do not promise contact delivery or Driver accommodation.
6. Controlled full-service deployment, physical app journeys and operational Support/SOS handling needed to close remaining staging/provider gates.



## Original audit inventory and evidence — before J-01–J-03 implementation

The following preserves the original pre-implementation audit findings and source references. Implementations and approved deferrals above supersede earlier absence findings across J-01–J-24; this historical appendix is not a completion tracker.

Journey	Definitely absent	Already implemented
J-03	Rider active-trip recovery/detail read; nearby-driver availability/count read.	Authenticated identity through /auth/me; internal matching and driver eligibility logic.
J-04–J-08	Ride Agent Gateway; place search/resolution/clarification contract; client-visible fare expiry; explicit availability/ETA model for options.	/trip/preview resolves a route through OSRM and generates fares from approved market pricing. /trip/start accepts a Rider-owned fare ID. Expired fares are rejected server-side.
J-09–J-16	Rider current-trip snapshot; complete assignment/vehicle/ETA read model; recoverable location read; Rider settlement/payment read; Rider command-result contract.	Rider WebSocket with replay; assignment and lifecycle events; driver-location events; cancellation; participant-scoped trip chat; trip issues; SOS; direct-payment settlement records/events.
J-17–J-18	Rider-owned paginated history, trip detail and receipt reads; rating eligibility/submitted-rating read.	POST /rider/trips/:tripID/rating, completed-trip/participant checks, persisted ratings, optional Rider comments, and one-star Admin review flagging.
J-19	Completed-trip metrics, received participant Driver feedback and recorded consumer snapshot history implemented locally; separate app-specific score intentionally absent without policy.	/auth/me exposes the available consumer trust snapshot, including score, status, calculation time and policy version.
J-20	Rider-owned name source/profile mutation; name-correction workflow; authenticated phone-change/reverification workflow.	Identity read, logout, and email-only PATCH /auth/account.
J-21	Rider payment instruments, generic wallet/ledger/payment activity APIs; ordinary-trip payment/settlement recovery read.	Finance-approved no-show debt reads, checkout creation and payment-status reads. Ordinary-trip direct-payment settlement exists internally.
J-23	Rider saved-place CRUD; privacy export/deletion request and status; trusted contacts; persisted safety/accessibility preferences.	Rider-owned persisted contracts implemented in J-23; shared reviewed deletion/private delivery primitives; live provider/delivery/device gates remain.
J-24	Rider notification preferences; device registration/removal; inbox/read state; push delivery pipeline/records; typed Rider notification targets.	Durable Rider WebSocket events. Notification/device/push infrastructure is implemented for Drivers.


The implemented trip routes are registered in [http.go (line 60)](/Users/abisoye/Projects/heyGo/backend/services/api-gateway/handler/http.go:60). Shared chat and safety routes are in [trip_chat.go (line 47)](/Users/abisoye/Projects/heyGo/backend/services/api-gateway/handler/trip_chat.go:47) and [support.go (line 60)](/Users/abisoye/Projects/heyGo/backend/services/api-gateway/handler/support.go:60).
Additional gaps that your checklist needs to capture explicitly
1. Rider role enrollment is missing from the app integration.
   New phone/email OTP accounts are created with empty roles. The backend has POST /auth/register, but Rider does not call it. Consequently, a newly authenticated account can reach Home while Rider-protected trip APIs—and role-gated Support—remain inaccessible. This affects J-02 → J-03, before the ride lifecycle starts. See [OTP account creation (line 348)](/Users/abisoye/Projects/heyGo/backend/services/api-gateway/handler/phone_auth.go:348).
2. Rider identity needs an initial name source, not only a correction API.
   Under the mounted session authentication, /auth/me obtains name from driver_profiles.display_name. A Rider-only account therefore has no Rider name source. Define initial name capture/storage separately from legal-name correction policy. See [readUser (line 196)](/Users/abisoye/Projects/heyGo/backend/services/api-gateway/auth/user.go:196).
3. Prevent multiple active Rider trips and repeated fare consumption.
   Trip creation generates a new trip for a valid fare. I found no Rider one-active-trip constraint/check and no single-use fare constraint. The existing idempotency middleware helps when the same key is reused; it does not prevent requests with different keys from creating multiple trips. Add these invariants to J-08/J-09, with recovery behavior. See [trip creation (line 66)](/Users/abisoye/Projects/heyGo/backend/services/trip-service/repo/postgres.go:66) and [core schema (line 12)](/Users/abisoye/Projects/heyGo/backend/shared/db/migrations/003_core_ride_state.sql:12).
4. Make “no drivers found” an authoritative, recoverable outcome.
   Matching publishes trip.event.no_drivers_found when it finds no candidate, but that branch does not transition the trip to a terminal state. A future active-trip read could therefore restore a trip still marked pending. Define the persisted outcome, matching deadline and retry/new-request behavior. See [matching consumer (line 91)](/Users/abisoye/Projects/heyGo/backend/services/driver-service/events/trip_consumer.go:91).
5. Complete the preview contract and error semantics.
   Fares expire after 15 minutes in the database, but preview DTOs omit expiresAt. The response also retains the legacy floating-point totalFareInPaise field; the gateway adds integer-Kobo amount/debt fields, but the contract needs consistent currency and units.
   Expired/invalid fares become a generic gateway trip-creation failure. Rider restriction and market-classification failures both map to trip_market_unavailable. These need distinguishable results so Rider can offer the correct recovery. See [protobuf contract (line 32)](/Users/abisoye/Projects/heyGo/backend/proto/trip.proto:32) and [gateway mapping (line 216)](/Users/abisoye/Projects/heyGo/backend/services/api-gateway/handler/http.go:216).
6. Finish assignment, location and pickup ETA contracts.
   Assignment events currently provide driver ID/name/photo/plate, but no complete vehicle description, assignment identifier or pickup ETA. Location events have coordinates and capture time, but no trip/assignment identifier. Location forwarding selects only accepted and started trips, excluding arrived. Add freshness and assignment-change handling to J-10–J-12. See [TripDriver (line 64)](/Users/abisoye/Projects/heyGo/backend/proto/trip.proto:64) and [location forwarding (line 99)](/Users/abisoye/Projects/heyGo/backend/services/driver-service/repo/driver.go:99).
7. Distinguish queued commands from applied results.
   Cancellation/rating return HTTP 202 with status: accepted after queueing. Participant/state validation occurs downstream. Cancellation emits an event when applied, but there is no general Rider rejection/result read contract; rating persistence also lacks a Rider submission read/acknowledgment. The UI needs confirmed, rejected and unresolved states. See [command handler (line 94)](/Users/abisoye/Projects/heyGo/backend/services/api-gateway/handler/http.go:94) and [consumer (line 69)](/Users/abisoye/Projects/heyGo/backend/services/trip-service/events/driver_consumer.go:69).
8. Define snapshot recovery alongside WebSocket replay.
   Replay already exists through afterEventId. However, a connection without a cursor starts from the latest event, which does not reconstruct the current trip. Add a Rider snapshot and a reliable snapshot-to-stream handoff; wire cursor persistence, reconnect and duplicate handling. See [event stream (line 43)](/Users/abisoye/Projects/heyGo/backend/services/api-gateway/handler/event_stream.go:43).
9. Use the actual launch payment policy.
   Ordinary fares are paid directly to the Driver. Completion opens a pending settlement; the Driver confirms or disputes it. That records the Driver’s assertion, not provider-verified payment. Rider needs a settlement read and accurate receipt states.
   The payment service contains checkout machinery, but I found no ordinary-trip lifecycle producer invoking PaymentCmdCreateSession. A wallet/card flow would require additional product and backend work. See [launch money rules (line 11)](/Users/abisoye/Projects/heyGo/backend/docs/ADMIN_LAUNCH_MONEY_RULES.md:11) and [settlement creation (line 351)](/Users/abisoye/Projects/heyGo/backend/services/trip-service/repo/postgres.go:351).
10. Include J-22 contract fixes and explicit J-13/J-15 integration.
    Generic Support is wired, but not fully correct:
    - Rider marks a case read without a body; backend requires throughMessageId. Because loadCase awaits that call, loading/reloading a conversation can fail.
    - Rider discards case/message pagination and message IDs.
    - Rider parses top-level error fields, while backend returns {error: {code, message}}.
    - Trip chat, trip-linked issues and SOS still need their existing APIs connected to real trip IDs.
    See [Rider client (line 164)](/Users/abisoye/Projects/heyGo/apps/rider/lib/services/rider_api.dart:164), [case loading (line 96)](/Users/abisoye/Projects/heyGo/apps/rider/lib/features/account/account_controller.dart:96), and [required read payload (line 908)](/Users/abisoye/Projects/heyGo/backend/services/api-gateway/handler/support.go:908).
Also add session-refresh failure/concurrency handling to J-01: Rider’s current refresh path does not clear the session when refresh fails during an authenticated operation, and concurrent requests can independently attempt refresh-token rotation.
Verification: focused route-registration, auth-normalization, rating-validation and payment-session tests passed. Several other package tests passed with database integration tests disabled. The broader run could not pass because the sandbox blocks local HTTP/WebSocket test listeners. Database, staging and provider behavior were not executed. No implementation files were changed during the audit.
## Email provider follow-up — 2026-10-14

- [x] Confirm Resend as primary email delivery and SMTP as an optional email fallback (not SMS).
- [x] Wire persisted Admin enable/disable, primary selection and fallback controls into Rider email sign-in/email-link OTP and staff-access emails; preserve existing settings sections and Rider UI.
- [x] Encrypt SMTP passwords server-side, require TLS, and avoid cross-provider fallback on uncertain acceptance.
- [ ] Verify real Resend/SMTP receipt and authenticated Admin controls once configured.
- [ ] Extend privacy-export email provider controls only with the existing durable export retry/idempotency guarantees preserved; exports currently retain Resend.

Contract and evidence scope: [EMAIL_DELIVERY_SETTINGS.md](EMAIL_DELIVERY_SETTINGS.md).

## Kudi SMS and channel availability — 2026-10-14

- [x] Wire Kudi's delivery-only SMS OTP adapter while retaining backend-owned verification, expiry and attempts.
- [x] Implement Admin-owned encrypted Kudi configuration and persisted SMS enable/disable controls.
- [x] Expose safe public authentication-channel availability; enforce disabled channels at OTP request and contact-change sends.
- [x] Bind mounted Rider onboarding to availability, show the enabled alternative and block both-disabled/unknown/stale-disabled submissions.
- [ ] Configure Kudi's approved Corporate Sender ID/App Name Code/OTP Template Code and verify actual +234 delivery.
- [ ] Apply migrations 062/063 and verify installed-device/Admin visual behavior.
- [ ] Approve correction of existing onboarding script's stale preview-only OTP wording; layouts and other old wording remain preserved.

Details: [KUDI_SMS_SETTINGS.md](KUDI_SMS_SETTINGS.md).
