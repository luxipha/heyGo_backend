# Backend operations

## Deployment decisions

- Commands and events use Google Cloud Pub/Sub in `heygo-ng`; no Kafka cluster is required.
- Production initially targets one region. A later multi-region design must address PostgreSQL ownership, Pub/Sub routing, payment-webhook routing, and WebSocket affinity together.
- Images use the repository release tag `0.1.0`; release automation must update the tag instead of deploying `latest`.

## Required configuration

Backend services require `GCP_PROJECT_ID` (or `GOOGLE_CLOUD_PROJECT`) and
Application Default Credentials with their service-specific Pub/Sub IAM grants.
Terraform provisions topics, subscriptions, ordering, retry policies, and
matching `.dlq` topics; applications never create messaging infrastructure at
startup. Local development may use `PUBSUB_EMULATOR_HOST`.

Driver OAuth code exchange additionally requires `CASPERID_API_SECRET`,
`CASPERID_TOKEN_URL`, and `CASPERID_DRIVER_REDIRECT_URI` on API Gateway. The
redirect URI must be registered exactly on the CasperID business app. Never put
the API secret in either mobile application.

Generate a local ignored secret manifest without placing credentials on the command line:

```sh
export POSTGRES_URL='postgres://...'
export INTERNAL_SERVICE_TOKEN='...'
export CASPERID_APP_ID='...'
export CASPERID_API_SECRET='...'
export MONNIFY_API_KEY='...'
export MONNIFY_SECRET_KEY='...'
export MONNIFY_CONTRACT_CODE='...'
./scripts/install-secrets.sh
```

Do not commit `infra/kubernetes/dev/secrets.yaml`. Rotate the internal service token, CasperID secret, database credentials, and Moniepoint credentials through the cluster secret manager in production.

Driver Operating Balance checkout uses `PAYMENT_SERVICE_URL` on API Gateway
(`http://localhost:9200` in local development, the payment-service cluster
address in Kubernetes). API Gateway and payment-service must receive the same
`INTERNAL_SERVICE_TOKEN`; the payment service's internal checkout endpoint
rejects requests without it. Configure `MONNIFY_BASE_URL`,
`MONNIFY_REDIRECT_URL`, API key, secret key, contract code, and the production
Monnify webhook. The redirect is a return path only: a signed webhook followed
by server-side transaction verification posts the Operating Balance credit.
CasperID must include the driver's `email` claim in its signed business JWT.
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
Deploy migration `016` before the new trip and driver services. Payment and
location notifications still need a separate direct-publication audit.

Driver and rider sockets now deliver from the shared `user_events` table in
cursor order. `015_driver_socket_presence.sql` adds an expiring Driver socket
session so Go Online and matching work when the socket and HTTP request reach
different gateway replicas. Deploy the migration before the new gateway code,
and upgrade all gateway replicas before relying on ordered delivery. Each
socket currently polls Postgres every 250 ms. A local run with 500 simulated
sockets and two gateway handlers measured about 1,995 reads/second with 658 μs
p95 database-read latency; this is a development-machine baseline, not a
capacity guarantee. Measure on deployment-equivalent infrastructure and
introduce a shared wake-up mechanism before scaling to a large number of
concurrent sockets. The `TEST_DATABASE_URL` integration tests cover takeover
and replay across two independent gateway handlers; exercise them with
separately running gateway processes and offer delivery before production
rollout.

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
