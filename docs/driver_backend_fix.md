# Driver backend status and remaining work

This replaces the earlier gap audit, which predated the Driver profile,
onboarding, documents, Home, matching, and Operating Balance work. It describes
the **current backend working tree**, not a deployed or end-to-end verified
system. The target app contract is
[`apps/driver/docs/DRIVER_API_REQUIREMENTS.md`](../../apps/driver/docs/DRIVER_API_REQUIREMENTS.md).

## Implemented in backend code

| Area | Current backend |
| --- | --- |
| Shared foundation | CasperID business JWT authentication; common response/error envelopes including OAuth exchange; optional authenticated HTTP idempotency replay; ordered database event log, cursor recovery and per-gateway delivery from shared Postgres; shared Driver socket presence. Trip creation, matching offers, assignment, expiry/decline/reassignment, acceptance acknowledgement, start, completion and cancellation now save Kafka events in the same transaction through migration `016`; the publisher retries with stable event IDs. Migrations, Kafka retry/replay, concurrent event ordering, and socket takeover/replay across two gateway handlers passed against disposable local PostGIS/Kafka. Separate-process staging verification remains. Payment and location notifications still need a separate direct-publication audit. |
| Session, onboarding, and devices | Driver profile, vehicle/package, document, inspection, onboarding and authoritative eligibility APIs are wired in the app; local logout clears token/socket, and 401 responses expire the local session. Driver document images use pre-signed R2 uploads. HeyGo device registration/removal routes are implemented with actor-scoped delete. Live CasperID NIN/email claim confirmation remains; FCM backend delivery is implemented, while Driver-app token acquisition/registration is not wired yet. |
| Driver identity and onboarding | Profile, vehicle and package routes; seven-requirement eligibility; HeyGo staff accounts, document/inspection review, and final approval. CasperID NIN verification must be explicit. |
| Documents and inspection | Private Cloudflare R2 upload reservations, driver submissions/resubmissions, HeyGo staff review, and staff upload/review of the independent inspection partner's emailed physical report. |
| Home and matching | Dashboard; server-owned availability; GPS-derived market; driver route; active-trip recovery; offer acknowledgement, expiry and reassignment. Admin geofence versions classify trips at creation and fail closed when required geography is unavailable. |
| Operating Balance | Admin-approved market minimum/warning/negative and top-up bounds; 30-minute GPS rule for online, matching and offline top-ups; balance read; driver-scoped, cursor-paginated ledger history; Monnify checkout initiation and status read; authenticated webhook with server-side verification and idempotent ledger credit. No policy or statutory rate is seeded. |
| Statutory charges | Admin draft/list/detail/two-person approval/retirement routes; validated fixed, percentage, progressive-tier and restricted-formula calculations; market/region/tag/package selection; whole-naira upward rounding; completion-time snapshots and Operating Balance debits; unpaid charge tracking, balance-floor enforcement, arrears-first confirmed top-ups, and eligibility blocking. No statutory rule or rate is seeded. |
| Trip, history, receipt, and earnings core | Driver-started trip with no prepayment; recorded arrival; queued completion/cancellation/rating commands; validated Driver-authored feedback tags; rider-authored optional comments up to 250 characters; one-star Rider ratings are flagged for Admin review; cursor-paginated Driver reviews/comments and Admin review queue; driver-scoped active-trip/detail reads; cursor-paginated trip history with cancellation actor and no-show claim status; completed-trip receipt read; one-time completed-trip earnings accrual; Today's Earnings in the Africa/Lagos calendar. Completion atomically opens a pending direct-payment settlement for the rounded trip fare. Driver can read it and manually confirm or dispute; events are transactional. This records the driver's assertion only, without bank verification. |
| Driver earnings reports | `GET /driver/earnings?period=day|week|month` returns accrued fare, commission, earnings, completed trips, average per day, zero bonus amount, prior-period totals and aligned chart series in the Africa/Lagos calendar. Day compares with the same elapsed hours yesterday; week compares rolling seven-day windows; month-to-date compares the same elapsed dates in the prior month. This is reporting over completed-trip accruals, not a payout or Operating Balance. |
| Account and privacy | Driver performance percentile (among drivers with at least 25 Rider ratings), profile/vehicle/package, and safety-contact APIs; account preferences are not yet implemented. Driver export creates a private ZIP of HeyGo records and private documents, emails a seven-day link to the CasperID-verified email through Admin-configured Resend, and exposes request status. Account deletion is a pending staff-reviewed request; approval blocks active trips/offers, deactivates the account, removes/anonymizes personal profile data, preserves trip/financial records, tombstones CasperID identity hashes, and queues private R2 objects for deletion. CasperID's signed `email_verified: true` claim is required for export. |
| Notifications and devices | Paginated Driver inbox/unread count, mark-one/all-read, owner-scoped device routes, and durable `notification.created` events. Document/inspection/approval, trip/settlement, no-show, top-up and statutory balance events enqueue inbox and socket notifications transactionally. FCM HTTP v1 worker claims deliveries with leases, retries transient errors, and removes unregistered device tokens. Credentials use a mounted Firebase service-account JSON file via `FCM_SERVICE_ACCOUNT_FILE` and optional `FCM_PROJECT_ID`. |
| Rider-driver chat | Text-only trip message history/send/read APIs for the rider and accepted driver; active-trip send gate, participant authorization, duplicate client-message protection, read receipts, 30-day read-only post-trip access, and durable `trip.message.created` / `trip.message.read` events using the shared event log. Masked calls are deferred until a telephony provider is selected. |
| Rider/Driver issues and support | Shared support topics; trip-linked issue reporting and general support cases; private R2 evidence reservations and ownership-checked completed uploads; cursor-paginated cases and case messages; idempotent case messaging; read receipts; durable user events for case creation and staff replies/status changes. HeyGo staff can list/search by status, read and reply, assign cases, change status, manage topics, and mark messages read. Staff replies and case/topic changes are written to the Admin audit log. |

The authenticated Rider/Driver routes are `GET /support/topics?kind=support|trip_issue`,
`POST /support/uploads`, `POST /support/tickets`, `POST /trips/:tripID/issues`,
`GET /support/cases`, `GET /support/cases/:caseID`,
`GET /support/cases/:caseID/messages?cursor=&limit=`,
`POST /support/cases/:caseID/messages`, and
`POST /support/cases/:caseID/read`. Admin routes under `/admin` are
`GET/POST /support/topics`, `PATCH /support/topics/:code`,
`GET /support/cases`, `GET /support/cases/:caseID`,
`GET /support/cases/:caseID/messages`, `POST /support/cases/:caseID/messages`,
`PATCH /support/cases/:caseID`, and `POST /support/cases/:caseID/read`.
Admin mutations use the staff session and CSRF protections.

## Still to implement

1. **Statutory charge launch readiness:** configure legally reviewed rule values,
   market policies and geofences, then verify the Admin approval and live
   completion/top-up flow in a deployed test environment. Rider/platform-funded
   drafts cannot be approved until their settlement collection flow is built.
   No values are seeded.
2. **Trip journey:** cancellation fee outcomes and the corresponding policy.
   No-show claim review and debt handling, trip history, receipts, rating tags,
   and their database reads/writes are implemented and locally integration-tested.
   `POST /trips/:tripID/complete` still returns `202 accepted` for its
   queued command; read settlement after `trip.event.completed`. The plural mutation routes exist;
   unused singular lifecycle aliases have been removed; rider preview/start
   paths remain because `apps/web` calls them.
3. **Remaining Driver APIs:** masked contact,
   and account preferences. A separate Driver Wallet, payout-account, and
   withdrawal API are not required for the current direct rider-to-driver
   payment model: per-trip manual confirmation/dispute and the distinct
   Operating Balance APIs cover the agreed launch flows. Revisit payouts only
   if HeyGo later collects rider fares or becomes responsible for paying
   drivers. Trip chat and support APIs are implemented. Account export/deletion APIs are
   implemented; the Admin UI still needs to call the Resend-configuration and
   deletion-review endpoints. Rider name/photo in offers await CasperID's exact
   profile contract.
4. **App integration:** the standalone Flutter Driver app still uses local
   package, inspection, availability, no-show, history, and wallet/earnings
   presentation state. It calls OAuth, `/auth/me`, registration, start, and the
   plural completion route, but has not adopted active-trip recovery, arrival
   reporting, or settlement reads/confirmation/dispute. Connect these when the
   corresponding API and product rules are ready. The Driver app must obtain
   FCM tokens and call device registration/removal as part of app lifecycle.
5. **Environment verification:** apply migrations to disposable PostGIS;
   configure approved launch geofences and balance policy with two staff
   accounts; exercise CasperID email/NIN claims, R2, Kafka/WebSocket recovery
   and socket takeover across multiple gateway replicas, configure
   `HEYGO_ADMIN_SETTINGS_KEY` as a 32-byte key encoded as 64 hexadecimal
   characters, then set and test Resend sender/API credentials in Admin;
   configure Firebase service-account credentials/workload identity and verify
   FCM delivery; OSRM, and Monnify checkout/webhook/ledger against configured
   services.

Active-trip PostGIS tests verify arrival -> explicit start -> completion,
rounded-fare pending settlement creation, outbox recipients, and manual
confirmation/dispute idempotency. Integration tests that require
`TEST_DATABASE_URL` skip without it. A passing Go suite does not prove the live
journey.

For current route details, see
[`DRIVER_HOME_MATCHING.md`](DRIVER_HOME_MATCHING.md),
[`DRIVER_GEOFENCES.md`](DRIVER_GEOFENCES.md), and
[`DRIVER_DOCUMENT_REVIEW.md`](DRIVER_DOCUMENT_REVIEW.md).
