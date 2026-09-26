# Cloud Run deployment

HeyGo publishes one immutable container image per backend service to:

```text
europe-west1-docker.pkg.dev/heygo-ng/heygo/<service>:<git-sha>
```

The image workflow builds every service on pull requests. After a protected
`main` merge, GitHub authenticates through Workload Identity Federation and
pushes the exact commit SHA. It never publishes a mutable `latest` tag.

## Runtime contract

All four services prefer Cloud Run's injected `PORT` and listen on every
interface. When `PORT` is absent, their existing local and Kubernetes settings
remain compatible:

| Service | Local fallback | Protocol | Intended access |
| --- | ---: | --- | --- |
| `api-gateway` | `HTTP_ADDR=:8080` | HTTP/WebSocket | Public |
| `driver-service` | `GRPC_ADDR=:9100` | gRPC/h2c | Internal |
| `trip-service` | `GRPC_ADDR=:9000` | gRPC/h2c | Internal |
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
- CasperID application ID and API secret;
- Monnify API key, secret key, and contract code for `payment-service`;
- service URLs for API Gateway to call Driver, Trip, and Payment services.

Optional R2, FCM, Resend, OpenTelemetry, CORS, and administrator settings should
be configured before enabling their corresponding product features.

## Deployment boundary

Terraform now defines Cloud SQL, Secret Manager, and all four Cloud Run
services, but `deploy_services` defaults to `false`. Follow the two-phase
procedure in `infra/terraform/README.md`: apply the database and secret
containers, populate external CasperID and Monnify secret versions, and only
then enable Cloud Run with an immutable image SHA.

The current subscribers and outbox publishers are long-running background
loops, so the initial Cloud Run configuration uses always-allocated CPU and at
least one instance. Converting delivery to authenticated Pub/Sub push endpoints
is the later scale-to-zero optimization. API Gateway is capped at one instance
until WebSocket delivery has durable fan-out.

Driver and Trip use gRPC over Cloud Run HTTP/2 and remain IAM-protected. API
Gateway uses its runtime identity to obtain Google-signed ID tokens for those
calls. API Gateway and Payment Service accept unauthenticated Cloud Run ingress;
Payment must be reachable by Monnify, while application authentication and the
shared internal token continue to protect their routes.

After the initial Terraform deployment, use the manually dispatched `Deploy
Cloud Run` GitHub Actions workflow with a full published Git SHA and the exact
confirmation value `deploy`. It creates tagged candidate revisions with no
production traffic, checks the public `/ready` endpoints, and only then moves
traffic to the latest revisions. Configure GitHub's `production` environment
with required reviewers before the first rollout. Terraform intentionally
ignores later container-image changes so releases do not create configuration
drift.
