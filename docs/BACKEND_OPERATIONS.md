# Backend operations

## Open work

These items are open until verified in the target environment; code or a local
test alone does not close an operational task.

- [ ] **Roll out Admin roles before enabling live staff work.** Apply migrations
  `036_admin_roles.sql`, `037_safety_incidents.sql`, `038_admin_multi_roles.sql`,
  and `039_staff_access_audit.sql`;
  explicitly assign the needed `operations`, `compliance`, `support`, `finance`,
  `safety`, or `super_admin` roles to each existing staff account.
  Existing accounts intentionally receive no automatic role and their domain
  requests return 403 until assigned. Create new accounts with
  `go run ./cmd/admin-user <username> <comma-separated roles>`. One second
  staff account may hold several domain roles, including Finance. Verify the
  role union, a denied ungranted request, and distinct-person approvals in
  staging before moving Admin out of preview.
- [ ] **Enable staff email access in staging.** Set `ADMIN_FRONTEND_URL` to the
  Admin HTTPS origin and configure/test Resend in Admin. Verify Super Admin
  invitation, email verification for an existing CLI-created account, expired
  and replayed links, password recovery, and session revocation. The Admin
  frontend must consume fragment tokens on its staff access pages.
- [ ] **Finish the Admin read models after the initial connection.** The staff
  Trip, Rider, and Partner reads now provide approved linked context, but the
  overview, timestamped Live Operations snapshot, Operating Balance entry read,
  and Safety incident queue remain initial contracts rather than full operations
  or financial reporting models. Further Finance charge/top-up/arrears reads
  still need their own agreed contract.
  The audit read now includes typed targets and redacted before/after fields
  where captured, but does not yet provide a complete trail across every
  domain action.
- [ ] **Verify Rider moderation in staging.** Apply migration
  `041_rider_moderation.sql` before deploying the gateway and Trip service.
  Confirm Safety and Super Admin can apply and lift a restriction only with a
  Driver-opened Safety case linked to the Rider's Trip. Confirm a Rider-opened
  complaint is rejected, new Trip creation is blocked, an existing Trip is
  unaffected, and Support cannot manage Safety flags. Check the Admin audit
  events and explicit review workflow; review dates do not lift restrictions
  or resolve flags automatically.
- [x] **Implement governed money actions in backend source.** Support owns
  fare/payment case intake and conversation; Finance receives typed handoffs
  and source-linked Operating Balance reads. Full successful Driver top-up
  refunds use an approved policy, distinct proposer and approver, a Driver-
  supplied verified bank destination, a reserved balance, provider status
  reconciliation, and an append-only ledger reversal and action history. The
  daily limit follows the Africa/Lagos calendar day. A Support case outcome
  never moves money. Local PostGIS and mocked-provider tests pass; see
  [ADMIN_LAUNCH_MONEY_RULES.md](ADMIN_LAUNCH_MONEY_RULES.md).
- [ ] **Release-gate refunds.** `HEYGO_MONNIFY_REFUNDS_READY` defaults to false
  in checked-in deployment manifests. Apply migrations 042–047, verify the
  merchant's sandbox and live activation, original payment method, regular
  bank destination, fee, signed terminal webhook, status lookup, and uncertain
  response handling with controlled provider transactions before enabling the
  flag. Keep the admin encryption key stable so bank destinations and reviewed
  legal names remain decryptable. Monitor `needs_reconciliation` and retained
  reservations; do not retry a claimed provider send without status evidence.

- [x] **Implement Admin-managed Kudi SMS and email routing in source.**
  Phone OTP uses the encrypted Kudi settings, not generic `SMS_PROVIDER_*`
  environment variables. Resend/SMTP routing is independently Admin-managed.
- [ ] **Verify intended-environment authentication delivery.** Apply migrations
  062–063; configure the approved Kudi sender, app/template codes and API key
  through Admin, then observe real +234 OTP receipt and HeyGo verification.
  Configure a verified email sender and observe inbox receipt/OTP verification.
  Enable a channel only after its controlled delivery check. See
  [KUDI_SMS_SETTINGS.md](KUDI_SMS_SETTINGS.md) and
  [EMAIL_DELIVERY_SETTINGS.md](EMAIL_DELIVERY_SETTINGS.md).
- [ ] **Configure the launch market and payments.** Publish Admin market
  boundaries and Operating Balance top-up bounds; supply Monnify credentials,
  redirect, and webhook configuration, then verify a signed webhook and
  server-side transaction check with a real checkout.
- [x] **Implement payment event recovery in source.** Migration 064 stores the
  immutable topic, recipient and payload in the same transaction as the payment
  update. A bounded, identity-protected payment drain and once-per-minute
  Scheduler job recover unpublished rows with their original event IDs. Local
  pull mode has a periodic recovery loop. Backlog metrics and alert rules cover
  aging rows, missing legacy envelopes and failed backlog reads.
- [ ] **Verify deployed payment event recovery.** Apply migration 064 before
  deploying Payment Service. Inspect legacy unpublished rows that could not be
  reconstructed; do not silently mark them delivered. Observe an interrupted
  publication, scheduled recovery, duplicate deduplication and alert delivery in
  cloud staging. See [PAYMENT_EVENT_RECOVERY.md](PAYMENT_EVENT_RECOVERY.md).
- [x] **Wire Ride Agent into the release configuration.** Terraform defines its
  protected Cloud Run service, dedicated identity, internal-token and Places-key
  secrets, gateway URL/audience, and gateway-only runtime invocation. The image
  release includes its candidate health check. It has no database/Pub/Sub grants.
- [ ] **Verify Ride Agent in cloud staging.** Populate the restricted server-side
  Places key, deploy all five service images and the migration image from one
  reviewed SHA, verify gateway identity plus internal-token authentication and
  real destination lookup/confirmation. LLM planning remains disabled pending
  its separately reviewed provider configuration. Review attribution/retention.
- [ ] **Finish best-effort location freshness handling.** The gateway persists
  the latest driver coordinate before publishing and includes its saved
  `recordedAt` time in rider updates. Update rider clients to discard older
  positions and show when the map is stale; verify this across reconnects.
- [ ] **Verify event operations in the deployed environment.** Alert on
  non-empty Pub/Sub dead-letter topics and aging trip outbox rows. Test replay,
  takeover, and offer delivery with separate gateway processes; measure socket
  load on deployment-equivalent infrastructure before raising concurrency.
- [ ] **Complete release checks.** Run the Go test, vet, static analysis, and
  vulnerability commands below. Exercise migrations and integration tests with
  disposable PostGIS and the Pub/Sub emulator; verify Admin Resend delivery and
  mobile token storage separately. Import the dashboard and alert rules into
  the chosen monitoring stack.
- [ ] **Complete the staged Cloud Run rollout.** Populate required Secret
  Manager versions and deployment inputs, run the migration job, deploy immutable
  images, verify authenticated Pub/Sub push and service readiness, then remove
  retained pull-consumer IAM grants. Follow `infra/terraform/README.md` for the
  ordered procedure.

## Deployment decisions

Admin authorization checks each registered `/admin` route against the union of
roles on the staff account at request time; role changes revoke its sessions.
`role` remains the primary-role field for older clients and `roles` returns
all grants. Roles apply across the launch market; there is no team or
per-market staff hierarchy yet. Super Admin can execute every domain action, while proposal and
approval of the same money or policy change require different staff IDs. Compliance
owns Driver review and deletion, Finance owns monetary rules and claims,
Operations owns geofences, Safety owns rating review, Support owns cases, and
Super Admin owns staff accounts and Resend settings. Super Admin alone can read
the redacted audit feed. `/admin/auth/me` returns `role`, `roles`, and the
union of permissions.
`GET /admin/trips`, `GET /admin/trips/:tripID`, `GET /admin/riders`, and
`GET /admin/riders/:riderID` are read-only, cursor-paginated where listed, and
exclude private contacts and route history. The Driver review endpoints still
contain sensitive identity data and are limited to Compliance and Super Admin.
The Trip list accepts `status`, `riderId`, and `driverId` filters so a staff
Rider or Driver record can link to its trips without copying trip history into
an account response.
The Trip detail keeps its existing top-level fields and adds `links` for
persisted milestone timestamps, assignment attempts, quoted fare and market
classification, quoted route distance in meters and duration in seconds,
current Driver vehicle profile, accepted-vehicle snapshot, future status events,
Driver-asserted settlement and ratings, with Support cases and Finance-owned
claims, charges, and debt payments behind their module permissions. The
vehicle object says `source: current_driver_profile`; the separate
`vehicleSnapshot` was frozen at acceptance when available. `historyCoverage`
reports whether events have been captured since Trip creation. Migration 040
does not backfill old events or vehicles. Raw route geometry, contact data, and Support message text
are omitted. Rider list search accepts only an exact Rider, Trip, or
Rider-owned Support case UUID in `q`; Rider detail links debts for Finance,
cases for Support, and incidents for Safety, with Super Admin inheriting all.
Safety or Super Admin can restrict new Trip creation for a Rider with a
Driver-opened Safety case linked to that Rider's Trip, a Rider-specific reason,
and a future review date. Only an audited Safety/Super Admin lift restores new
Trip access. Support owns case-linked support/payment flags; Safety owns
incident-linked flags from Driver-opened Safety cases. Super Admin can manage
both. Flags require a reason and review date and never restrict a Rider by
themselves. The corresponding list and mutation routes are under
`/admin/riders/:riderID/restrictions` and `/admin/riders/:riderID/flags`.
Production Admin has no Rider account-creation route, including for Super
Admin; use isolated test fixtures to simulate Rider registration.
Quoted route geometry and traveled location history remain closed to Admin at
launch. Historical fare, market, and policy evidence needs staging validation;
old vehicle snapshots and Trip events cannot be reconstructed.
`GET /admin/drivers` accepts exact Driver ID or display-name prefix in `q`,
review `status`, `online=current|not_current`, `zone` as the published operating
market code, `balance=negative|zero|positive|missing`, and
`expiry=expired|within_30_days|valid|missing`. Online means a requested online
state, active socket, and location recorded within 30 seconds; no coordinate is
returned. Missing balance is distinct from zero. Expiry uses the earliest
expiry of the latest approved submission for each of the five required document
types; missing or unapproved required documents form the `missing` band. The
list includes `X-As-Of` and a cursor. Compliance can filter and read only a
balance band; exact kobo on this Partner route requires both Partner and Finance
read permissions (or Super Admin).
Partner detail adds recent Trip IDs/statuses, performance, and case/incident
IDs with safe status, priority, severity, and destinations. Compliance can see
these summaries, while messages and incident evidence remain behind the owning
module permissions. `GET /admin/drivers/:driverID/verification-timeline`
returns paginated application, document, inspection, and review events with
safe metadata and redacted audit changes. Its `meta.coverage=partial_legacy`
marks older gaps; it must not be presented as a complete or tamper-proof audit.
Reviewer assignment and SLA are deferred.
New fare quotes convert OSRM duration seconds to minutes before applying the
per-minute component. Previously stored quotes retain their original amounts.
Compliance and Super Admin can read paginated Driver document submission and
inspection history through `/admin/drivers/:driverID/documents/history` and
`/admin/drivers/:driverID/inspections/history`; these omit storage keys and
file URLs. Existing short-lived file access remains on its separate guarded
route.
`GET /admin/overview` and `GET /admin/live/drivers` are Operations/Safety
polling contracts with a 10-second refresh hint. Driver coordinates carry
`recordedAt`; presence is current under 30 seconds, stale from 30 to under 60
seconds, and disconnected after 60 seconds or a lost socket. The live list
omits contact details and vehicle plates.

Overview attention items contain type, entity ID, priority, owner role,
optional assignee, destination API path, and creation time. Each type's count
and first 20 items come from one database snapshot using the same predicate
as the URL in `attentionQueues`; Support and Safety accept the `attention`
status filter. `GET /admin/live/markers` returns up to 100 current
request pickup snapshots to Operations and up to 100 active-trip incident
markers to Safety; incident coordinates come only from an assigned Driver
with a current socket and a location under 30 seconds old. Each marker says
where and when its location was recorded. A missing marker is not proof that
no request or incident exists.

Safety topics create an incident linked to the Support case; Safety owns incident decisions and Support retains
the case conversation. Finance-only reads under `/admin/finance/` expose
Operating Balance entries, fare/payment case metadata, and disputed settlement
snapshots without money actions.

Super Admin can create a pending account with `POST /admin/staff/invitations`
and resend a pending invitation with `POST /admin/staff/:staffID/invite`.
`PATCH /admin/staff/:staffID` requires a 5–500 character `reason` alongside
role or active-state changes; its audit record has a typed staff target and
redacted before/after roles and activity.
`POST /admin/staff/:staffID/email-verification` lets a Super Admin attach a
verified recovery email to an existing CLI-created account. Public
`POST /admin/auth/accept-invite`, `/admin/auth/verify-email`,
`/admin/auth/recovery`, and `/admin/auth/reset-password` consume single-use,
hashed tokens. Invitation and verification links expire in 24 hours; recovery
links expire in 15 minutes. Accepting an invitation or resetting a password
revokes existing sessions. The email links use fragment tokens and require
`ADMIN_FRONTEND_URL`; the frontend must clear the fragment after reading it.
The invite body is `{username,email,roles}`. Accept/reset bodies are
`{token,password}`; email verification accepts `{token}` and recovery request
accepts `{email}`. Public recovery always responds as accepted for a valid
email format, including unknown accounts. Only verified, active staff email
addresses receive a recovery link. All roles currently apply across the
launch market.
Driver contact fields require `drivers.contact.read` (Compliance or Super
Admin). Support or Super Admin can read a requester's contact only through
`GET /admin/support/cases/:caseID/contact`, which records an audit event.
Operations, Finance, and Safety do not have either contact permission.

- Commands and events use Google Cloud Pub/Sub in `heygo-ng`; no Kafka cluster is required.
- Production initially targets one region. A later multi-region design must address PostgreSQL ownership, Pub/Sub routing, payment-webhook routing, and WebSocket affinity together.
- Production images use full Git commit SHA tags. The Kubernetes development
  manifests still use `0.1.0`; `scripts/build.sh` defaults to that local tag.

## Required configuration

Backend services require `GCP_PROJECT_ID` (or `GOOGLE_CLOUD_PROJECT`) and
Application Default Credentials with their service-specific Pub/Sub IAM grants.
Terraform provisions topics, subscriptions, ordering, retry policies, and
matching `.dlq` topics; applications never create messaging infrastructure at
startup. Local development may use `PUBSUB_EMULATOR_HOST`.

Phone and email sign-in share `OTP_PEPPER`. Both require
`HEYGO_ADMIN_SETTINGS_KEY` for encrypted Admin-managed provider configuration.
Email OTP uses the saved Resend/SMTP routing; phone OTP uses saved Kudi settings.
Generic `SMS_PROVIDER_URL`, `SMS_PROVIDER_TOKEN`, and `SMS_SENDER_ID` are
legacy compatibility settings and no longer select the production sender.
Migration 063 defaults SMS to disabled until explicitly configured/enabled.
Never put provider credentials or the OTP pepper in either mobile application.

Ride Agent requires a server-side `RIDE_AGENT_PLACES_KEY` and a generated
`RIDE_AGENT_INTERNAL_TOKEN`. Gateway uses `RIDE_AGENT_URL` plus
`RIDE_AGENT_AUDIENCE` to authenticate through Cloud Run IAM while retaining
application-level internal-token authentication. LLM planning is disabled in
the checked-in deployment configuration.

Generate a local ignored secret manifest without placing credentials on the command line:

```sh
export POSTGRES_URL='postgres://...'
export INTERNAL_SERVICE_TOKEN='...'
export OTP_PEPPER='...'
export HEYGO_ADMIN_SETTINGS_KEY="$(openssl rand -hex 32)"
export MONNIFY_API_KEY='...'
export MONNIFY_SECRET_KEY='...'
export MONNIFY_CONTRACT_CODE='...'
./scripts/install-secrets.sh
```

Do not commit `infra/kubernetes/dev/secrets.yaml`. Rotate the internal service
token, Ride Agent token/Places key, OTP pepper, optional CasperID secrets,
database credentials, and Monnify credentials through Secret Manager in Cloud Run production. Back
up `HEYGO_ADMIN_SETTINGS_KEY`; changing it without a credential migration makes encrypted Resend, SMTP, Kudi,
bank destinations and reviewed legal names unreadable.

Driver Operating Balance checkout uses `PAYMENT_SERVICE_URL` on API Gateway
(`http://localhost:9200` in local development, the payment-service cluster
address in Kubernetes). API Gateway and payment-service must receive the same
`INTERNAL_SERVICE_TOKEN`; the payment service's internal checkout endpoint
rejects requests without it. Configure `MONNIFY_BASE_URL`,
`MONNIFY_REDIRECT_URL`, API key, secret key, contract code, and the production
Monnify webhook. The redirect is a return path only: a signed webhook followed
by server-side transaction verification posts the Operating Balance credit.
Phone-authenticated users may save an account email through `PATCH /auth/account`;
email OTP must still be completed before that address becomes verified.
Payment checkout requires a syntactically valid saved email; sensitive data
exports continue to require a separately verified email.

The OTP session contract is:

- `POST /auth/email/otp/request` with `{ "email": "user@example.com" }`, or
  `POST /auth/otp/request` with `{ "phone": "+234..." }`, returns a short-lived
  `challengeId`; the same destination is limited to three requests per ten minutes.
- Verify through the matching `/auth/email/otp/verify` or `/auth/otp/verify`
  endpoint with `challengeId`, the same email or phone, the six-digit `code`,
  and optional `deviceName`. Either route creates or restores the same type of
  HeyGo account and returns a 15-minute access token plus a rotating 30-day
  refresh token.
- `POST /auth/refresh` rotates both tokens. Authenticated `POST /auth/logout`
  revokes that session. Store both tokens in platform secure storage, never in
  logs or ordinary preferences.
- `POST /auth/register` adds the `rider` or `driver` role. Driver registration
  creates the pending Driver profile; it does not grant permission to go online.

Admin must publish market boundaries and an Operating Balance policy with
top-up bounds before drivers can create a checkout in that market. No sample
minimum, statutory charge, or market boundary is seeded.

## Event operations

All event envelopes use schema version `1`, a unique event ID, and a correlation
ID. `EntityID` is the Pub/Sub ordering key. A failed delivery is negatively
acknowledged; each subscription retries with bounded backoff and forwards the
message to `<topic>.dlq` after five delivery attempts. Alert on non-empty
dead-letter topics and replay only after fixing the handler or data contract.

Migration `016_trip_event_outbox.sql` stores core trip transitions, offers,
expiry, decline, reassignment and retry events in `trip_event_outbox` with the
state change. Trip and driver service
replicas share one advisory lock while publishing pending rows in ID order.
The publisher waits for Pub/Sub acknowledgement before setting `published_at`;
a crash can send the same stable `trip-outbox-<id>` event twice, so consumers
must remain idempotent. Monitor `SELECT COUNT(*), MIN(created_at) FROM
trip_event_outbox WHERE published_at IS NULL` and investigate an aging backlog.
Deploy migration `016` before the new trip and driver services. The payment and
location publication findings and remaining decisions are tracked above.

Driver location updates are best-effort live telemetry. The latest persisted
reading is authoritative; an individual missed rider notification does not
require replay when a newer reading arrives. Clients must use the reading time
to avoid showing an old coordinate as current. Trip-state and payment events
retain durable delivery requirements.

Driver and rider sockets now deliver from the shared `user_events` table in
cursor order. `015_driver_socket_presence.sql` adds an expiring Driver socket
session so Go Online and matching work when the socket and HTTP request reach
different gateway replicas. Deploy the migration before the new gateway code,
and upgrade all gateway replicas before relying on ordered delivery. Each
gateway instance uses PostgreSQL `LISTEN/NOTIFY` to wake local sockets,
with a 15-second recovery poll when the listener is available and a 2-second
poll otherwise. The older 250 ms polling load result is no longer a current
capacity baseline. Measure on deployment-equivalent infrastructure before
raising the gateway's concurrency limit. The `TEST_DATABASE_URL` integration
tests cover takeover and replay across two independent gateway handlers;
exercise them with separately running gateway processes and offer delivery
before production rollout.

## Health and observability

- `/health` is a liveness endpoint.
- `/ready` verifies required runtime dependencies for HTTP services.
- `/metrics` exports Prometheus HTTP request and latency metrics.
- Trip and driver services expose the standard gRPC health service.
- Import `infra/observability/grafana-dashboard.json` and load `infra/observability/prometheus-rules.yaml` into the monitoring stack.
- `X-Correlation-ID` is returned to callers and propagated through gRPC and Pub/Sub.

## Release verification

Run `go test ./...`, `go vet ./...`, `staticcheck ./...`, and `govulncheck ./...`.
With `TEST_DATABASE_URL` pointing at a disposable PostGIS database, tests execute
migrations and verify required tables and the extension. Pub/Sub integration
tests additionally require `TEST_GCP_PROJECT_ID` and `PUBSUB_EMULATOR_HOST`.

### SMS configuration supersession — 2026-10-14

The earlier generic SMS environment-variable examples in this document are historical. Production API Gateway now reads Kudi SMS configuration from Admin Settings after migration 063; `SMS_PROVIDER_URL`, `SMS_PROVIDER_TOKEN`, `SMS_SENDER_ID` do not select the production sender. Keep `OTP_PEPPER` and `HEYGO_ADMIN_SETTINGS_KEY` configured. SMS stays disabled until Admin saves the Kudi key and approved Corporate Sender ID/App Name Code/OTP Template Code and enables it. See [KUDI_SMS_SETTINGS.md](KUDI_SMS_SETTINGS.md) for live gates and safe public channel availability.
