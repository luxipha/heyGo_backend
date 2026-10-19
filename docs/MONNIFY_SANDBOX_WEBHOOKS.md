# Monnify sandbox callbacks

Monnify documents that sandbox callbacks omit `monnify-signature`:
https://developers.monnify.com/docs/webhooks

The payment webhook accepts an unsigned collection callback only when the payment handler is configured with the exact server-side provider origin `https://sandbox.monnify.com`. Production and unrecognised origins still require the existing HMAC-SHA512 signature. A supplied invalid signature is never treated as an unsigned sandbox callback.

Unsigned callbacks are lookup hints. Before state changes, the handler requires a locally initiated payment/top-up/debt record, checks any stored transaction reference, queries Monnify through the authenticated API, and matches payment reference, transaction reference, currency, total payable, terminal status, and the full paid amount for successful collections. Pending statuses cannot be promoted to success or failure. Unknown references do not trigger provider requests. Provider failures return 503; mismatches return 409. Existing repository idempotency and payment-event recovery remain in use.

Refund, disbursement, settlement, mandate, wallet and bill notifications are not enabled through the unsigned sandbox exception. Refund callbacks remain signature-only. Do not populate unsupported dashboard webhook fields.

## Validation and rollout

Run payment handler, service and repository tests with `TEST_DATABASE_URL` pointing only at an isolated test database. The database identity integration test is skipped when that variable is absent. Retain all current payment regression and duplicate-event checks.

Deploy only the new payment-service image after required CI succeeds. No migration, credential change or frontend rebuild is needed. Keep `MONNIFY_BASE_URL=https://sandbox.monnify.com`; the webhook URL is unchanged. When changing to the live provider origin later, unsigned callbacks are automatically rejected.

Complete a real sandbox checkout and inspect signed-in HeyGo payment status and duplicate notification handling before claiming provider end-to-end verification. Unit tests and mocked-provider tests alone are not that evidence.
