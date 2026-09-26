# HeyGo Google Cloud foundation

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

```sh
terraform -chdir=infra/terraform plan -out=runtime-foundation.tfplan
terraform -chdir=infra/terraform apply runtime-foundation.tfplan
```

Terraform generates and stores versions for `heygo-database-url`,
`heygo-migration-database-url`, and `heygo-internal-service-token`. The
long-running services use the database role with read/write privileges;
the migration job alone receives the elevated database credential. Add values
for the externally owned credentials
without placing their plaintext in Terraform variables, GitHub, or the repo:

```sh
read -rs 'SECRET_VALUE?CasperID app ID: '
printf '%s' "$SECRET_VALUE" | gcloud secrets versions add heygo-casperid-app-id --data-file=- --project=heygo-ng
unset SECRET_VALUE

read -rs 'SECRET_VALUE?CasperID API secret: '
printf '%s' "$SECRET_VALUE" | gcloud secrets versions add heygo-casperid-api-secret --data-file=- --project=heygo-ng
unset SECRET_VALUE

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

## Phase 2: Cloud Run

Use a full Git commit SHA whose five images already exist in Artifact Registry.
First set `image_tag` while keeping `deploy_services = false`, plan, and apply
to create the migration job. Run the schema migrations before any services
start:

```sh
gcloud run jobs execute heygo-migration --project=heygo-ng --region=europe-west1 --wait
```

Then set `deploy_services = true`, `allowed_origins`, and `app_url` in an
ignored `terraform.tfvars`, review the plan, and apply again. The runtime
services set `MIGRATE_ON_STARTUP=false`; local development keeps its existing
automatic migration behavior.

Driver and Trip remain IAM-protected. API Gateway obtains Google-signed ID
tokens from its runtime identity for gRPC calls, in addition to the existing
application-level internal token. API Gateway is public. Payment Service is
also public because Monnify must reach its webhook; its `/internal/*` routes
continue to require the internal token.

All four services currently run pull subscribers or background outbox workers,
so they use always-allocated CPU and a minimum instance count of one. API
Gateway is temporarily capped at one instance because its Pub/Sub subscription
feeds in-memory WebSocket connections. This is operationally correct but not
the final low-cost topology; authenticated Pub/Sub push delivery and durable
WebSocket fan-out are required before scale-to-zero or multi-instance Gateway.

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
