# Rider verification evidence

Review date: 2026-10-12 (Asia/Ho_Chi_Minh). This records the evidence level for every journey group and its downstream handoff. A checked implementation task does not close a provider/device/deployment gate.

## Journey evidence and downstream handoffs

| Journey | Source/contracts | Automated evidence | Seeded/local staging evidence | Provider/live and downstream gate |
| --- | --- | --- | --- | --- |
| J-01–J-02 auth/enrollment | OTP, Rider registration, role/session checks, refresh rotation and revocation. | Real router/PostgreSQL phone/email enrollment, rotation/revocation and temporary database failure; Flutter concurrent refresh, retry identity, restore and invalid-refresh cases. | Named seeded Rider signs in via real HTTP; phone OTP intercepted in memory; actual refresh and logout. | Real SMS/email delivery and installed secure-storage restart remain unobserved. |
| J-03 Home/recovery | Owned current/detail snapshots and matching-rule availability. | Ownership, no-trip response, actual market/eligibility freshness, recovery/races and Home widget blocking/unavailable states. | Owned active-trip/history/Profile reads from the seeded database. | Actual device location, app resume/restart and deployed availability need observation. |
| J-04–J-08 Agent/places/preview/start | Structured Agent schema, approved tools/fallback, authoritative preview/fare/expiry/Kobo, confirmation and atomic fare/trip rules. | Agent/tool restrictions, market pricing, quote errors, route mapping, fare single use and concurrent Rider starts; Flutter retry/expiry/edit cases. | No configured live route/place stack was exercised. | OSM/Google selection/retention/attribution explicitly deferred; live pricing/route/preview/start handoff remains open. |
| J-09–J-12 matching/live | Durable matching deadline/outcome, assignment snapshot, location freshness, cursor handoff/replay. | Terminal no-driver/deadline, assignment/location filtering, event commit order, offer expiry/reassignment, lifecycle and stream recovery tests. | Actual local Pub/Sub publish while consumer offline, consumer startup/recovery and replay deduplication; real authenticated seeded Rider WebSocket replay. | Full deployed matching/assignment/location/lifecycle plus device background/reconnect remain open. Emulator IAM is not cloud IAM evidence. |
| J-13–J-15 chat/cancel/issues/SOS | Participant chat/read window, explicit cancellation command result, owned issues/SOS/no-show status and Support context. | Chat pagination/restrictions/read events; queued/result/lost command recovery; SOS idempotency/location; reviewed cancellation/no-show and Support/Finance authorization. | Actual seeded staff login, Support case read/reply and Rider message/inbox read-through. | No real operational responder/staff service SLA, deployed cancellation interruption or device SOS flow was observed. Intake remains acknowledgment. |
| J-16–J-18 settlement/history/receipt/rating | Owned reads, real receipt reference/Kobo breakdown, direct-payment truth, rating eligibility/ack, fresh rebook. | Persisted settlement/no-show/rating/history/receipt ownership and one-star behavior; Flutter wire/receipt/money/pagination/recovery. | Owned seeded history reads. | Full trip completion → direct settlement → receipt → rating app journey remains open. Driver confirmation is not provider-verified success; rebook still needs configured places. |
| J-19 Profile/trust | Completed metrics, approved received feedback, consumer snapshot/history. | Counts/ownership, allowed tags, snapshot metadata/unavailable/clearing and pagination. | Actual owned seeded metrics read. | Authenticated device visual parity and upstream trust calculation/provider handoff remain unobserved. |
| J-20 identity correction | Approved optional self-managed Rider display name, Support legal-review entry, owned current/new phone proofs with all-session revocation, and owned verified-email linking. | PostgreSQL ownership, Driver-name isolation, expiry/attempts/foreign proofs, concurrent single-use phone confirmation, session revocation and email-linking protection; mounted name-save/local credential-clear tests. | Authenticated controlled HTTP profile write/identity recovery and intercepted email-link verification; migration 061 applied. | Real current/new SMS/email delivery, staff/provider legal-review handling and installed contact recovery remain unobserved. |
| J-21 approved launch money | Direct Driver fares and provider-collected Finance-approved debt; owned debt/payment history/status/checkout. Wallet/cards and ordinary-fare collection deferred by user approval. | Verified-email gates at gateway/service, owned pagination/detail, stable-key recovery after restart, explicit confirmation/changed-debt checks, one-time ledger posting and uncertain provider-create recovery. Card fields cannot collect data while deferred. | Authenticated controlled HTTP debt/payment recovery reads; no live checkout was sent. | Real provider checkout/webhook/browser-return and operator reconciliation remain unobserved; no generic wallet/card product is claimed. |
| J-22 generic Support | Owned paginated cases/messages/replies/read acknowledgments with stable IDs and context. | User/staff workflow, errors, pagination, lost create/reply IDs and private attachment rules. | Real limited staff credentials/CSRF/cookie auth, actual seeded case reply, Rider reads and inbox/stream handoff. | Complete installed app UI, actual operational staff handling and configured private attachments if needed. |
| J-23 account/privacy/safety/accessibility | Persisted places/contacts/preferences, private Rider export, reviewed account-wide deletion. | Ownership/version/provider-unavailable, ZIP allowlist/expiry, deletion/Driver regressions and failed-write/reread tests. | Seeded database migrated through 060. | Actual place providers, private storage/email worker, reviewed staff cleanup and installed accessibility/restart remain open. Contact delivery is unavailable. |
| J-24 notifications | Owned prefs/session-bound devices/inbox, transactional fan-out/retry, typed recovery and native bridge. | Concurrent dedup, ownership/cursors/read scope, rotated-token safety, session suppression, account-deactivation race, leases/stale results; controlled FCM adapter; Flutter pref/read/tap/owner/wire/widget tests. | Actual seeded HTTP preferences/device, staff reply → inbox/read → authenticated Rider stream; local Pub/Sub lifecycle → inbox hint replay. | No real push acceptance/device receipt. Android Firebase package mismatch, missing iOS plist, APNs/sender/device/deployed scheduling configuration are open. |

## Executed checks

- Broad backend suites passed for gateway, Trip, Driver, Ride Agent and shared packages with local PostGIS configured. Final JSON run: **202 test/subtest passes**, zero failures, one deliberate performance/load-test skip (`TestSocketPollingLoad`). Pub/Sub emulator replay was executed, not skipped in this final run.
- Final Rider unit/widget suite covers account persistence, notifications, Home, preview/start, trip lifecycle/chat/settlement/history/receipt, Support/Profile, refresh and recovery. Final full run: **72 tests passed**; source/widget evidence does not establish installed-device behavior.
- Focused Flutter analysis, backend/Rider whitespace checks and Terraform messaging formatting were run.
- Android debug build was attempted and failed at the existing Gradle 7.6.3 / Java 21 incompatibility (`Unsupported class file major version 65`). Its automatic migrator changes were reverted; the known Android Firebase package mismatch also remains open. No Android build success is claimed.
- iOS simulator build succeeded. It was installed/launched on iPhone 17 / iOS 26.0; the mounted initial Agent onboarding screen was visually inspected. This was an unauthenticated onboarding smoke check.
- Canonical Notifications settings and new Inbox rendered at 430×932 in the real app theme/navigation. Readable widget screenshots were inspected with explicitly loaded test text/icon fonts. Existing three-row settings layout is retained, new controls use Rider tokens and the inbox card uses the existing border/radius tokens. This is controlled widget visual evidence, not native font/layout parity or a real provider session.
- A separate real-router HTTP command exercised the seeded staging database with limited staff auth and real Rider session/refresh/WS ownership. It uses an in-memory OTP recorder, no external SMS, and synthetic device tokens; no provider push is sent.

Private local artifacts (not committed): `/private/tmp/heygo-rider-j24-all-backend-results.jsonl`, `/private/tmp/heygo-rider-j24-flutter-results.jsonl`, `/private/tmp/heygo-rider-j24-staging-check.log`, `/private/tmp/heygo-rider-j24-ios-build.log`, `/private/tmp/heygo-rider-j24-android-build.log`, `/private/tmp/heygo-rider-j24-visual/`, `/private/tmp/heygo-rider-j24-native.png`. Artifacts are not credentials; do not add private `.dev` configuration, raw tokens or session cookies to public evidence.

## Repeat local staging checks

From backend, against the existing disposable database only:

```sh
RIDER_STAGING_DATABASE_URL='postgres://abisoye@127.0.0.1:55439/heygo_rider_staging?sslmode=disable' go run ./scripts/rider-staging-seed
RIDER_STAGING_DATABASE_URL='postgres://abisoye@127.0.0.1:55439/heygo_rider_staging?sslmode=disable' go run ./scripts/rider-staging-check
```

The second command starts a temporary loopback HTTP gateway with production routes/session middleware; binds the documented synthetic `+2348012345678` only to the guarded seeded Rider; intercepts OTPs in memory; authenticates `rider-staging-support` using the private password file; adds a controlled staff case reply; rotates/logs out the Rider session and removes its synthetic device. It retains the staff/inbox evidence in this disposable database. Normal OTP limits still apply across repeat runs. It does not attach Trip/Driver services or create real provider credentials.

The official local Pub/Sub emulator was installed and run at `127.0.0.1:8086`, project namespace `heygo-rider-staging-local`; no GCP project/resources were provisioned. It was stopped after verification; restart it to repeat the check. Test with:

```sh
gcloud beta emulators pubsub start --project=heygo-rider-staging-local --host-port=127.0.0.1:8086
TEST_DATABASE_URL='postgres://abisoye@127.0.0.1:55439/heygo_rider_auth_test?sslmode=disable' TEST_GCP_PROJECT_ID=heygo-rider-staging-local PUBSUB_EMULATOR_HOST=127.0.0.1:8086 go test ./shared/messaging -run TestOutboxPubSubGatewayReplayDeduplicates -count=1
```

The emitter publishes while its consumer is offline, verifies that receipt has not been observed, starts the actual consumer, verifies lifecycle plus inbox hint, republishes the same outbox event and verifies no duplicates. This is local service interruption/replay evidence; no deployed broker was interrupted.

## Outstanding completion gates

Rider IDE analyzer follow-up (2026-10-12): full `flutter analyze` reproduced 13,275 diagnostics, all under generated `build/` dependency trees, including iOS FlutterFire and macOS Swift Package Manager checkouts. Rider `analysis_options.yaml` now excludes `build/`, `.dart_tool/` and native Pods; `rider.code-workspace` also excludes them from IDE analysis. Existing inactive legacy-source exclusions are unchanged. Full `flutter analyze --no-pub` then passed with **No issues found** (3.4 seconds). Logs: `/private/tmp/heygo-rider-ide-analyze.log` and `/private/tmp/heygo-rider-ide-analyze-fixed.log`. This correction changes analyzer configuration only; no UI or dependency changes were required. Restart the Dart Analysis Server if the IDE retains cached diagnostics.

- [ ] Run the full authenticated installed Rider journey: restart/resume, Agent preview/expiry/edit, no-driver/new request, assignment/live location, cancellation command recovery, completion/direct settlement, receipt/rating/rebook and Support context.
- [ ] Observe real intended-environment OTP, OSM/Google route/place behavior, matching/sweeper/Pub/Sub/Cloud Tasks recovery and push delivery independently.
- [ ] Verify Android/iOS device permission/restart/token/background behavior and approved screen parity. No physical device was available here.
- [ ] Observe J-20 real delivery/staff/provider review and J-21 approved provider checkout/reconciliation under the now-approved policy. Deferred wallet/cards and ordinary-fare collection require a future product decision if reopened.
- [ ] Configure/observe private privacy delivery, actual trusted-contact delivery if approved, and operational Support/SOS handling.

Source payload protections: Ride Agent tools remain allowlisted; account/wallet/privacy/notifications/Admin operations are excluded. Rider notification contents are constructed from typed resource hints, not raw event/route/body/amount data. Device tokens remain server-private, privacy archives are owned/private, and gateway credential query parameters are normalized/removed before logging and tracing. These boundaries have focused tests; this is not a claim of a deployed whole-system security certification.


## J-20/J-21 implementation follow-up (2026-10-12)

The approved policy is recorded in `RIDER_IDENTITY_MONEY_POLICY.md` and the journey checklists in `RIDER_REVIEW.md`. Rider identity uses the explicit `riderDisplayName` field of `/auth/me`, preserving the generic Driver name on dual-role accounts. Backend contact confirmation locks the live session and account; only one concurrent phone confirmation applies. Legacy unverified Rider aliases cannot silently become login links. Preserved Home end/payment callbacks now recover authoritative state rather than advancing local lifecycle state.

- **Automated:** full Rider analyzer reports **No issues found**; **82 Rider unit/widget tests pass**, including empty/dual-role name handling, owned settings-to-legal-review context, persisted checkout retry identity, changed debt/unverified email rejection, explicit confirmation, credential removal, and read-only card fields. Gateway handler, payment handler and payment repository suites pass with the isolated real PostgreSQL database; auth store behavior is exercised by those handler tests. They include expiry/attempts/foreign proofs, concurrent phone proof consumption, session revocation, verified-email linking/revalidation, owned 30+1 payment pagination and foreign/Driver denial, unresolved provider-create retry without a second initializer call, and duplicate verified settlement posting. Logs: `/private/tmp/heygo-rider-j20-analyze-final.log`, `/private/tmp/heygo-rider-j20-flutter-final.log`, `/private/tmp/heygo-rider-j20-backend-final.log` and `/private/tmp/heygo-rider-j20-payment-final.log`.
- **Controlled local staging:** the final guarded HTTP check passed persisted Rider name/identity recovery, intercepted email proof/linking, debt/payment reads and existing auth/Support/inbox/stream handoffs; migration 061 is applied. The gateway closes and cleans its device/session. Log: `/private/tmp/heygo-rider-j20-staging-final.log`. This is not deployed service-stack or provider delivery evidence.
- **Native:** the exact final Rider source builds for the iOS simulator (`/private/tmp/heygo-rider-j20-ios-final.log`). A compiled build was installed/launched on the booted iPhone 17 / iOS 26.0 simulator. `devicectl list devices` reports no physical devices. Compilation/startup and mounted widget tests do not establish authenticated installed old/new widget parity. Android retains its previously documented toolchain gate.
- **Provider/live:** no real SMS/email, no-show payment, bank/card transaction or staff legal-identity verification was sent/performed. These gates remain unchecked. Wallet/cards/ordinary-fare collection are explicitly deferred, not implemented/enabled.

## Email provider routing follow-up — 2026-10-14

Source: migration 062 adds persisted Admin Resend/SMTP toggles, primary selection, explicit fallback and encrypted write-only SMTP credentials. Rider email sign-in/email-link OTP and staff access consume the saved route. Privacy exports retain their Resend-specific idempotent worker, explicitly identified in the UI/docs.

Automated: focused gateway tests passed with a newly created disposable local PostGIS database (`heygo_email_test`, loopback port 55441): migration/defaults/persistence/password preservation, provider ordering/disabled states, TLS/STARTTLS SMTP acceptance and unresolved DATA outcomes, existing Resend encryption, Rider enrollment/rotation/revocation and contact verification. Admin API-client suite: **14 tests passed**, including routing endpoints/CSRF/write-only password. Full Admin TypeScript check remains blocked by the existing missing `components/partners/PartnerTripsTab` import; no successful full Admin build or visual parity is claimed.

Provider/live: no real email, SMTP credentials/server, inbox receipt, delivery webhook or failover observation. Configure the intended environment and run controlled inbox/OTP verification before closing those gates. Apply migration 062 before deploying the new gateway/Admin endpoints.

The full gateway handler suite passed against this disposable PostGIS database after the routing changes (16.181s): `/private/tmp/heygo-email-backend-tests.log`. Whitespace checks passed for the changed backend/Admin files. The temporary database server was stopped after verification.

## Kudi SMS and public authentication channels — 2026-10-14

Source: migration 063; encrypted Admin Kudi key/approved sender/app/template configuration, enable/disable, production SMS sender and Rider phone-change sender; safe uncached GET `/auth/channels`; mounted Rider disabled-channel/alternate-method/retry/stale-entry behavior. Existing OTP verification/session rules remain authoritative. See [KUDI_SMS_SETTINGS.md](KUDI_SMS_SETTINGS.md).

Automated: full gateway handler suite passed against disposable PostGIS (12.151s), including Kudi multipart acceptance/error parsing, Admin persistence/secret preservation/default disabled state/public safe availability/new-request disable enforcement; gateway main package checked. Full Rider suite **87 tests passed**, including five new channel DTO/widget tests. Rider analyzer clean. Admin API-client suite **15 tests passed**. Full Admin typecheck remains blocked by the existing missing `PartnerTripsTab` import. Logs: `/private/tmp/heygo-kudi-backend-final.log`, `/private/tmp/heygo-kudi-rider-all-final.log`, `/private/tmp/heygo-kudi-rider-analyze-final.log`.

Provider/live/deployment: no real SMS/test phone was contacted; Kudi key, Corporate Sender ID, App Name Code, Template Code, provider approval and actual +234 delivery/verification remain open. Apply migrations 062/063 before deploying changes. Authenticated Admin/installed Rider visual checks remain open; existing preview-only script wording is recorded separately for approval. The disposable test server was stopped after verification.

## Deployment preparation — 2026-10-14

Ride Agent now has a protected Cloud Run definition, dedicated runtime identity,
server-only Places/internal-token secrets, gateway URL/audience and release
health check. No database or Pub/Sub access is granted to its runtime. LLM
planning remains disabled. Generic SMS deployment settings are superseded by
the existing Admin-managed Kudi configuration.

Payment migration 064 atomically saves terminal webhook event envelopes. A
bounded, OIDC-protected drain plus Scheduler recovery and backlog metrics/alerts
recover interrupted publication with stable IDs. Exact legacy evidence is
backfilled; unmatched rows remain visible for operator investigation. This
path does not enable ordinary Rider fare collection.

Executed locally against a separate disposable PostGIS database: full affected
Payment/Gateway handler suites and Ride Agent/shared auth suites passed;
race-enabled recovery/webhook tests passed, including acknowledgement loss,
concurrent recovery serialization, immutable replay and legacy backlog metrics.
The migration backfill test passed for exact versus mismatched legacy evidence.
Go vet, Terraform validation/formatting, workflow YAML/shell parsing and
whitespace checks passed. Provider, cloud IAM, Scheduler execution, actual
Places lookup and alert notification delivery remain unobserved. No cloud
resources were provisioned or deployed during this work.
