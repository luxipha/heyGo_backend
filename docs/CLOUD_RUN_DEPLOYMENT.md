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
- Kafka brokers and compatible client authentication;
- shared `INTERNAL_SERVICE_TOKEN` secret;
- CasperID application ID and API secret;
- Monnify API key, secret key, and contract code for `payment-service`;
- service URLs for API Gateway to call Driver, Trip, and Payment services.

Optional R2, FCM, Resend, OpenTelemetry, CORS, and administrator settings should
be configured before enabling their corresponding product features.

## Deployment boundary

Cloud Run services are not created by the foundation yet. PostgreSQL and Kafka
are paid, stateful dependencies whose sizing and networking must be selected
before deployment. Driver and Trip use gRPC and must have HTTP/2 end-to-end
enabled. Internal services should remain IAM-protected; API Gateway will need
Cloud Run Invoker grants and Google-signed ID tokens for synchronous calls.

After those dependency decisions are made, add the Cloud Run services to
Terraform, attach pinned Secret Manager versions, run migrations as a separate
release step, deploy the SHA-tagged images to staging with no production
traffic, and run `/health`, `/ready`, and authenticated service-to-service smoke
tests before promotion.
