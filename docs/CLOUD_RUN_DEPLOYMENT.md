# Cloud Run deployment

HeyGo publishes one immutable container image per backend service to:

```text
europe-west1-docker.pkg.dev/heygo-ng/heygo/<service>:<git-sha>
```

The image workflow builds every service on pull requests. After a protected
`main` merge, GitHub authenticates through Workload Identity Federation and
pushes the exact commit SHA. It never publishes a mutable `latest` tag.

## Choose the change path before deploying

| Change | Existing path | Terraform apply? |
| --- | --- | --- |
| Backend code or database migrations | Audited SHA, published images, `Deploy Cloud Run` workflow | No |
| Venue URL or the two venue key references | `Configure venue partner`: prepare, test, promote | No |
| Cloud SQL, networking, Pub/Sub, runtime IAM, or other foundation resources | Explicit infrastructure review and a fresh Terraform plan | Only after that specific plan is approved |

Terraform has been used to provision the foundation. It is not executed by routine
application releases. `GCP foundation` validates Terraform and verifies cloud
access; it does not plan or apply infrastructure. The configuration workflow adds
only its named settings, while Terraform may reconcile the entire foundation.
Do not choose a full apply simply because a feature needs an environment variable.

Before any future infrastructure apply, inspect the live service configuration,
secret names/versions, current remote state and the exact production inputs.
Never use the initial-provisioning defaults on an existing deployment:
`deploy_services=false` can propose removing services, and empty feature settings
can propose removing live environment entries. Preserve the enabled R2 storage,
FCM, Heroes URL and venue configuration. Keep actual key values out of source,
plan summaries, workflow inputs and logs. Add or update existing settings rather
than replace the complete environment/secret list.

Review every proposed deletion, replacement, environment removal, image/traffic
change and unrelated IAM change. Stop when the plan contains unexplained drift.
Do not use `-target` or blanket `ignore_changes` to hide unexplained differences.
Creating existing Secret Manager containers through Terraform requires deliberate
state reconciliation/import, not another create attempt. In particular, venue
secret containers may have been provisioned independently by the configuration
rollout. Recheck them and their access bindings before any later infrastructure
apply.

### Terraform reconciliation from 23 November 2026

The local Terraform edits were initially held out of the application and venue
configuration releases. The separate reconciliation preserves R2/FCM/Heroes and
pinned venue settings, imports the already-created venue containers/access grants,
and keeps workflow-owned images/revisions unchanged. Payment's optional Admin
settings key remains disabled unless explicitly enabled in a separate review.
The unrelated `QA_REVIEW_PHONE_CLEANUP.md` stays outside infrastructure releases.

Cloud audit logs confirmed that all 29 service subscriptions were automatically
removed on 19 November by `Subscriber.InternalExpireInactiveSubscription` under
their default 31-day inactivity policy. Service subscriptions now explicitly use
an empty expiration TTL so they do not expire. Restoration does not recover the
deleted subscriptions' backlog. Readiness checks publisher permission; they do
not prove subscription presence or functional delivery.

Use `scripts/terraform-production-plan.py` and the production reconciliation
procedure in `infra/terraform/README.md`. It reads current services/state, derives
private inputs without reading key values, saves a full refreshed plan, and rejects
changes outside the known restoration/import scope. The reviewed restoration plan
contains 29 subscription creates, 58 unchanged-scope access bindings and four
no-change imports, with no service, database, environment, image or traffic changes.
Do not apply an old plan or initial-provisioning defaults. Review the new saved
plan, retain state evidence, verify restored policies/identities and run another
full plan after apply; no remaining changes is the infrastructure completion gate.

## Runtime contract

All five services prefer Cloud Run's injected `PORT` and listen on every
interface. When `PORT` is absent, their existing local and Kubernetes settings
remain compatible:

| Service | Local fallback | Protocol | Intended access |
| --- | ---: | --- | --- |
| `api-gateway` | `HTTP_ADDR=:8080` | HTTP/WebSocket | Public |
| `driver-service` | `GRPC_ADDR=:9100` | gRPC/h2c | Internal |
| `trip-service` | `GRPC_ADDR=:9000` | gRPC/h2c | Internal |
| `ride-agent-gateway` | `PORT=8085` | HTTP | IAM-protected; Gateway only |
| `payment-service` | `PAYMENT_HTTP_ADDR=:9200` | HTTP | Public webhook plus authenticated internal routes |

Containers run as numeric user `65532:65532`. Application logs go to stdout by
default for Cloud Logging. `LOG_FILE` remains available as an explicit opt-in
for non-Cloud Run environments.

## Required deployment configuration

The services intentionally fail startup when required dependencies or secrets
are missing. Do not deploy a revision until these are ready:

- PostgreSQL/PostGIS and a `DATABASE_URL` secret;
- Pub/Sub topics/subscriptions from `infra/terraform` and `GCP_PROJECT_ID=heygo-ng`;
- shared `INTERNAL_SERVICE_TOKEN` secret;
- API Gateway `OTP_PEPPER`, `HEYGO_ADMIN_SETTINGS_KEY`, and configured Admin-managed
  Resend/SMTP settings for email OTP and Kudi settings for phone OTP; apply 062–063;
- Monnify API key, secret key, and contract code for `payment-service`;
- service URLs for API Gateway to call Driver, Trip, Payment, and Ride Agent;
- `RIDE_AGENT_INTERNAL_TOKEN` shared only by Gateway and Ride Agent, server-only
  `RIDE_AGENT_PLACES_KEY`, and gateway `RIDE_AGENT_AUDIENCE` for IAM authentication.

Optional CasperID trust delivery, R2, FCM, Resend, OpenTelemetry, CORS, and
administrator settings should be configured before enabling their corresponding
product features. Email/phone sign-in and manual Driver approval do not require
CasperID credentials.

Staff email invitations and recovery additionally require `ADMIN_FRONTEND_URL`
to be the Admin HTTPS origin and configured Admin Resend settings. The backend
sends single-use links with tokens in the URL fragment; the Admin client must
submit the token and password to the matching `/admin/auth/` endpoint. Verify
the frontend uses `Referrer-Policy: no-referrer` on these pages and clears the
fragment after reading it. Existing CLI-created staff need a Super Admin to
send an email-verification link before email recovery is available.

## Deployment boundary

Terraform now defines Cloud SQL, Secret Manager, and all five Cloud Run
services, but `deploy_services` defaults to `false`. Follow the staged
procedure in `infra/terraform/README.md`: apply the database and secret
containers, populate external Places and Monnify secret versions, create and run
the separate migration job, and only then enable Cloud Run with an immutable
image SHA. Keep `retain_pull_subscriber_permissions=true` through the first
Cloud Run rollout. After authenticated push delivery is verified, set it to
`false` in a separate cleanup plan. The services use a read/write database role
and do not run migrations in Cloud Run.

Production event delivery uses authenticated Pub/Sub push endpoints and
request-based Cloud Run billing with a minimum instance count of zero. Retained
pull IAM grants are a temporary rollback safeguard; they do not start pull
consumers or prevent push delivery. API Gateway remains capped at one instance
while WebSocket connections are held in memory.

Ride Agent uses protected HTTP; Gateway adds a Google identity token alongside
its separate application token. Its runtime has no database or broker access.
LLM planning remains disabled.

Driver and Trip use gRPC over Cloud Run HTTP/2 and remain IAM-protected. API
Gateway uses its runtime identity to obtain Google-signed ID tokens for those
calls. API Gateway and Payment Service accept unauthenticated Cloud Run ingress;
Payment must be reachable by Monnify, while application authentication and the
shared internal token continue to protect their routes.

After the initial Terraform deployment, use the manually dispatched `Deploy
Cloud Run` GitHub Actions workflow with a full published Git SHA and the exact
confirmation value `deploy`. It creates tagged candidate revisions with no
production traffic after the migration job succeeds, checks the public `/ready`
endpoints and protected Ride Agent `/health`, and only then moves traffic to those verified revisions. Configure GitHub's `production` environment
with required reviewers before the first rollout. Terraform intentionally
ignores later container-image changes so releases do not create configuration
drift.

These candidate checks do not establish a complete downstream journey: Gateway
readiness checks PostgreSQL and Pub/Sub, not candidate Trip/Driver behavior.
Require cloud-staging auth/matching/trip/payment/recovery evidence before
production promotion. Apply migration 064 before Payment Service and verify the
authenticated payment-event recovery Scheduler job. See
[archived payment-event recovery notes](https://github.com/luxipha/heyGo_backend/blob/98f4e8c88349b93b296d75d70e46691df762bac9/docs/PAYMENT_EVENT_RECOVERY.md).


## Venue partner configuration rollout

Use `Configure venue partner` for this configuration-only change. It preserves
all existing API environment entries, secret references, image and runtime identity;
it does not run migrations or apply Terraform. Its concurrency group is shared
with application releases. The exact workflow commit must have a passing main
audit. Provision the two external keys privately in Secret Manager and grant only
the API runtime identity access to those secrets before preparing a revision.

- `VENUE_PARTNER_URL`: HTTPS origin; the client appends `/api/heygo/...`.
- `VENUE_PARTNER_SERVICE_KEY`: numeric version of `heygo-venue-partner-service-key`;
  sent to the partner as `x-heygo-service-key`.
- `VENUE_PARTNER_INBOUND_KEY`: numeric version of `heygo-venue-partner-inbound-key`;
  the partner must send this independent key as `x-service-key` to
  `/partner/venue-earnings`.

Dispatch `prepare` with the current 100%-traffic revision, origin, numeric secret
versions and `confirmation=configure`. It saves rollback evidence, creates a
no-traffic candidate, rejects changes to existing configuration, and checks
readiness/authentication. Inspect the run artifact and test the candidate with
the saved keys using read-only partner queries and an authenticated malformed
earnings report (expect 400 without any database write). Do not use a real order
or send owner OTPs merely to check configuration.

Only after those checks pass, dispatch `promote` using the successful prepare run
ID and the same expected old revision. The workflow verifies the candidate and
production state again, promotes that explicit revision, and checks readiness.
Retain the workflow artifact. Roll back using `gcloud run services update-traffic`
with the artifact's `previousRevision`; secret versions and database are not
changed by traffic rollback. Never paste key values into workflow inputs or logs.
This proves configuration/authentication, not venue submission, delivery, or wallet
credit end-to-end. Configure the partner's earnings sender separately.
