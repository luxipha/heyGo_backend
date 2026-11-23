# HeyGo Google Cloud foundation

> Existing production: first read [the deployment change-path guide](../../docs/CLOUD_RUN_DEPLOYMENT.md#choose-the-change-path-before-deploying).
> Routine backend releases and venue configuration changes use their GitHub
> workflows. The initial provisioning examples below are not an instruction to
> apply Terraform to an already-running environment. Local reconciliation edits
> require a separate review and fresh plan; do not apply them to clear Source Control.

This Terraform root provisions the shared deployment foundation and runtime in the
`heygo-ng` project:

- required Google Cloud APIs;
- the `europe-west1` Docker Artifact Registry;
- Pub/Sub event topics, per-service subscriptions, dead-letter topics, and IAM;
- dedicated runtime service accounts for each backend service;
- a least-privilege GitHub deployment service account;
- keyless GitHub Actions authentication through Workload Identity Federation;
- a deletion-protected PostgreSQL 16 Cloud SQL instance with backups and
  point-in-time recovery;
- Secret Manager containers and least-privilege runtime access;
- a dedicated migration job with separate database credentials; and
- guarded Cloud Run services that use immutable image tags.

Runtime provisioning is deliberately staged. First create Cloud SQL, generated
database/internal credentials, and empty containers for external provider
secrets. Next create and execute the migration job. Only then create Cloud Run
services after external secret versions and deployment inputs exist.

## Phase 1: database and secret containers

Keep `deploy_services = false`, review the plan, and apply it. This creates a
billable `db-f1-micro` Cloud SQL instance. It does not create Cloud Run services.
Keep `retain_pull_subscriber_permissions = true` so this foundation phase does
not remove the existing pull-consumer IAM grants before push delivery exists.

```sh
terraform -chdir=infra/terraform plan -out=runtime-foundation.tfplan
terraform -chdir=infra/terraform apply runtime-foundation.tfplan
```

Terraform generates and stores versions for `heygo-database-url`,
`heygo-migration-database-url`, `heygo-internal-service-token`, and
`heygo-otp-pepper`, plus a stable 64-character hexadecimal
`heygo-admin-settings-key` used to encrypt Admin-managed Resend credentials. The
long-running services use the database role with read/write privileges;
the migration job alone receives the elevated database credential. Add values
for the externally owned credentials
without placing their plaintext in Terraform variables, GitHub, or the repo:

```sh
read -rs 'SECRET_VALUE?Monnify API key: '
printf '%s' "$SECRET_VALUE" | gcloud secrets versions add heygo-monnify-api-key --data-file=- --project=heygo-ng
unset SECRET_VALUE

read -rs 'SECRET_VALUE?Monnify secret key: '
printf '%s' "$SECRET_VALUE" | gcloud secrets versions add heygo-monnify-secret-key --data-file=- --project=heygo-ng
unset SECRET_VALUE

read -rs 'SECRET_VALUE?Monnify contract code: '
printf '%s' "$SECRET_VALUE" | gcloud secrets versions add heygo-monnify-contract-code --data-file=- --project=heygo-ng
unset SECRET_VALUE
```

Email and SMS provider controls are persisted through authenticated Admin.
Apply migrations 062–063 and configure Resend/SMTP and Kudi there; the old SMS
secret container is retained for compatibility but is not mounted into the
gateway. Preserve `heygo-admin-settings-key` across releases.

Terraform generates `heygo-ride-agent-internal-token`. Supply a server-only
Google Places key restricted to the intended Places API before enabling services:

```sh
read -rs 'SECRET_VALUE?Ride Agent Places key: '
printf '%s' "$SECRET_VALUE" | gcloud secrets versions add heygo-ride-agent-places-key --data-file=- --project=heygo-ng
unset SECRET_VALUE
```

The Ride Agent has its own runtime identity and no database or Pub/Sub IAM
grants. Only API Gateway has runtime invocation permission; the deployment
identity may call health for release checks. Gateway sends a Google ID token
in `X-Serverless-Authorization` and its distinct internal token in
`Authorization`. LLM planning remains disabled until separately configured.

## Phase 2: Cloud Run

Use a full Git commit SHA whose six images (five services plus migration) already exist in Artifact Registry.
First set `image_tag` while keeping `deploy_services = false`, plan, and apply
to create the migration job. Run the schema migrations before any services
start:

```sh
gcloud run jobs execute heygo-migration --project=heygo-ng --region=europe-west1 --wait
```

Then set `deploy_services = true`, `allowed_origins`, and `app_url` in an
ignored `terraform.tfvars`. Keep `retain_pull_subscriber_permissions = true`
for this first Cloud Run rollout, review the plan, and apply again. The runtime
services set `MIGRATE_ON_STARTUP=false`; local development keeps its existing
automatic migration behavior.

Verify that every subscription is configured for authenticated push and that
each service is successfully processing messages. Only after that verification,
set `retain_pull_subscriber_permissions = false`, review the cleanup plan, and
apply it. That final plan should remove the legacy runtime subscriber IAM grants
without changing the subscriptions or their push configuration.

Driver and Trip remain IAM-protected. API Gateway obtains Google-signed ID
tokens from its runtime identity for gRPC calls, in addition to the existing
application-level internal token. API Gateway is public. Payment Service is
also public because Monnify must reach its webhook. Public-service Pub/Sub and
maintenance endpoints additionally validate the event-invoker OIDC identity.

The original four services use authenticated Pub/Sub push and request-based Cloud Run
billing with a minimum instance count of zero. Driver-offer expiry uses a
dedicated Cloud Tasks queue. Cloud Scheduler invokes bounded gateway
maintenance once per minute and performs a once-per-minute transactional
outbox recovery pass. A separate once-per-minute payment-event recovery job
uses the pinned event-invoker identity and payment push audience. Apply
migration 064 first; inspect missing legacy envelopes and enable the payment
backlog alert rules. Ride Agent uses request billing without a broker. Each service is initially capped at one instance and the
global database pool budget is 16 connections.

API Gateway remains capped at one instance while it owns WebSocket connections
in memory. Its timeout is 3,600 seconds, and clients must reconnect with backoff
when deployments or timeout rotation close a socket. The
`api_gateway_concurrency` variable deliberately defaults to 80; raise it toward
400-500 only after a WebSocket load test. Until then, do not treat 300
simultaneously online drivers as supported. Open WebSockets keep the instance
active and billable, but the gateway can scale to zero when no connections or
requests remain. Committed user events wake local WebSocket streams through one
PostgreSQL `LISTEN/NOTIFY` connection per gateway instance; a 15-second indexed
poll is retained only as recovery protection if a notification is missed.

Once the services exist, application releases use the manually dispatched
`Deploy Cloud Run` workflow. Configure required reviewers on the GitHub
`production` environment. The workflow deploys candidate revisions without
production traffic after running database migrations, checks public readiness,
and promotes the verified revisions only after the checks pass. Terraform owns
runtime configuration while ignoring subsequent
image-only revisions made by that workflow.

## Trust boundary

The OIDC provider accepts tokens only when all of these claims match:

- repository ID `1369862034` (`luxipha/heyGo_backend`);
- repository owner ID `202601895` (`luxipha`); and
- branch `refs/heads/main`.

No service-account key is created or stored in GitHub.

## State

Terraform state is stored in the versioned, access-controlled GCS bucket
`heygo-ng-terraform-state` under the `foundation` prefix. The bucket is a
one-time bootstrap resource and is not managed by this root configuration.

## Commands

```sh
terraform -chdir=infra/terraform init
terraform -chdir=infra/terraform fmt -check
terraform -chdir=infra/terraform validate
terraform -chdir=infra/terraform plan -out=foundation.tfplan
terraform -chdir=infra/terraform apply foundation.tfplan
```

Review every plan before applying it. Never commit `terraform.tfvars`, state,
plan files, generated GitHub credentials, or application secrets.

## Default service account hardening

The automatically assigned Editor role has been removed from the unused Compute
Engine default service account. Confirm it remains absent with:

```sh
gcloud projects get-iam-policy heygo-ng \
  --flatten=bindings[].members \
  --filter='bindings.members:392496443029-compute@developer.gserviceaccount.com'
```

The project currently has no Google Cloud organization parent. Google only
allows `roles/orgpolicy.policyAdmin` at the organization level, so this project
cannot yet enforce the
`constraints/iam.automaticIamGrantsForDefaultServiceAccounts` preventive policy.
If the project is moved into an organization, grant the infrastructure operator
that role at the organization and add the constraint before creating any new
default service accounts.

The one-time role removal is intentionally not modeled as an authoritative
Terraform IAM binding because doing so would make this root responsible for
every project member that legitimately holds the Editor role.

## Existing production configuration and venue partner changes

Before planning against an existing deployment, read the live environment and secret
references and populate the production inputs. Preserve `r2_secret_version` (the
four separately provisioned `heygo-r2-*` secrets), `fcm_project_id` (keyless ADC),
and `heroes_public_url`. Empty defaults omit these environment entries and will
remove them from an existing service if the production inputs are omitted.
The R2 secret containers and permissions remain separately provisioned; this root
references their existing names without importing or replacing them.

`venue_partner_url` is an HTTPS origin without `/api`; the client adds that path.
Create/populate `heygo-venue-partner-service-key` before enabling outbound calls.
Inbound earnings reports use the separate `heygo-venue-partner-inbound-key` and
`venue_partner_inbound_enabled`; populate and coordinate that key with the partner
before enabling it. Terraform creates containers/access bindings, not external
credential values. Keep the keys out of tfvars, source and review output.

Application release workflows own images and generated revision names. Terraform
continues to manage environment, identities and other runtime configuration.
Review a fresh full plan for unrelated drift, resource removals, secret-reference
changes and missing Pub/Sub subscriptions before any apply. A successful image
rollout does not prove infrastructure or broker delivery is healthy.

## Production reconciliation and subscription restoration

`python3 scripts/terraform-production-plan.py --output .dev/NEW_REVIEW_DIRECTORY`
(from the backend root) prepares a new private evidence directory, reads current
services and state, derives production inputs without reading key values, and
creates a full refreshed saved plan. It never applies Terraform. It rejects
service updates, deletions, replacements, unexpected creates, and expiring
subscriptions. Raw state/plan material stays under ignored `.dev` with private
permissions. Use the saved plan hash and summary when reviewing an apply; never
print raw state or commit the generated tfvars/plan.

The November 2026 audit confirmed `Subscriber.InternalExpireInactiveSubscription`
for all 29 service subscriptions on 19 November. Their old policy was 31 days of
inactivity. These are permanent service wiring, so `expiration_policy.ttl = ""`
now prevents expiry. Restoring a subscription does not restore its deleted
backlog; do not claim historical message recovery from a successful apply.

The venue containers and API read grants already exist. `venue_imports.tf`
adopts those four resources using import blocks; secret values and externally
provisioned versions remain untouched. The source uses the pinned venue versions
verified by the configuration workflow. Do not replace them with `latest` during
reconciliation. Payment's optional Admin settings key is explicitly disabled for
the current direct-secret configuration and requires a separate opt-in review.

For this restoration, an acceptable plan has only 29 subscription creates, 58
existing-scope subscriber/dead-letter access bindings, and four no-change venue
imports. There must be no Cloud Run, Cloud SQL, traffic, environment, image,
credential-value, or unrelated IAM changes. Review and authorize the exact saved
plan before applying it; retain the prior state snapshot. After apply, verify all
29 push endpoints, pinned OIDC identity/audiences, non-expiring policies and access
bindings, then generate another full plan: it must show no remaining changes.
Run functional delivery tests separately with controlled actors; readiness alone
checks publisher permission and does not establish subscriber delivery.
