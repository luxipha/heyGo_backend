# Payment webhook event recovery

Migration **064_payment_event_recovery.sql** stores the topic, recipient and
minimal `PaymentStatusUpdateData` payload in `payment_webhook_events`, in the
same transaction that records the terminal payment status. Raw provider
metadata stays on the private payment record, outside the event envelope.

The webhook still attempts immediate publication. If publication or its final
database acknowledgement fails, the saved envelope remains recoverable. A
repeated webhook uses the immutable original envelope, even if the payment
record subsequently changes. Recovery publishes with the original event ID and
recipient ordering key; consumers must deduplicate at-least-once delivery.

`POST /internal/tasks/payment-events/drain` requires the Google-signed OIDC
identity of the configured `PUBSUB_PUSH_SERVICE_ACCOUNT`, validated against
`PUBSUB_PUSH_AUDIENCE`. Payment Service has public ingress for Monnify, so
Cloud Run ingress alone is not its authorization boundary. Terraform schedules
this endpoint once per minute, using the same pinned event-invoker identity and
the payment push audience. Requests are bounded to 25 seconds/100 events; each
publication has a ten-second deadline. A transaction-scoped advisory lock and
row lock serialize recovery workers until acknowledgement and commit.

Local pull mode runs periodic recovery in-process. Request-billed cloud push
mode relies on Scheduler so scale-to-zero does not strand unpublished rows.

## Legacy rows and monitoring

Migration 064 reconstructs legacy envelopes only where the stored webhook's
exact event type, transaction reference and terminal status match the event ID.
Unmatched unpublished rows remain pending and are skipped by the drain. Do not
delete or mark them delivered merely to clear an alert; investigate provider,
payment and downstream delivery evidence before an operator-led repair.

Payment `/metrics` exposes database-backed counts/age without payment IDs or
provider contents:

- `heygo_payment_events_pending`
- `heygo_payment_events_oldest_age_seconds`
- `heygo_payment_events_unrecoverable`
- `heygo_payment_events_backlog_read_success`

Alert definitions are in `infra/observability/prometheus-rules.yaml`. Import
them into the chosen monitoring system and verify scraping/notification routing;
checked-in definitions alone do not activate monitoring.

## Deployment gates

- Apply 064 with the migration identity before deploying Payment Service.
- Inspect `payment_webhook_events WHERE NOT published`, especially missing
  topic/recipient/payload values, before and after rollout.
- Verify the Scheduler request succeeds only for the pinned identity; foreign
  identities and unauthenticated requests must receive 401.
- Observe recovery following interrupted publication, stable-ID replay and
  downstream deduplication in cloud staging, then test alert delivery.

This recovery path covers the existing generic Trip payment webhook events.
Driver top-ups and Rider no-show debt payments retain their separately verified
ledger/reconciliation paths. It does not enable ordinary Rider fare collection,
refunds, wallet or cards.

## Executed local verification — 2026-10-14

Real disposable PostGIS tests passed for atomic event persistence, retry after
lost acknowledgement, concurrent recovery serialization, original-envelope
replay after a later payment change, duplicate drain suppression and missing
legacy-envelope metrics. Recovery/webhook tests also passed with the race
detector. Migration backfill tests passed for exact evidence while preserving
mismatched rows. Unauthenticated/wrong-identity drain tests passed; Google
OIDC validation is covered by the shared authorizer tests. No real provider or
cloud recovery/alert delivery was exercised.
