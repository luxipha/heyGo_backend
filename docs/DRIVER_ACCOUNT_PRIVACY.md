# Driver account privacy operations

This describes the backend APIs and configuration for Driver data export and
account deletion. The Admin frontend has not yet been wired to these endpoints.

## Resend setup in HeyGo Admin

Before a staff member can save Resend credentials, set
`HEYGO_ADMIN_SETTINGS_KEY` as a deployment secret containing 64 hexadecimal
characters (32 random bytes). Generate a value with `openssl rand -hex 32`.
The gateway uses this AES-256-GCM key to encrypt the Resend API key before it is
stored in Postgres. The API key is never returned by the Admin read endpoint or
written to the audit log. Back up this key securely; losing it makes the stored
Resend key and encrypted archive links unreadable.

The staff Admin APIs require an authenticated HeyGo staff session. Mutations
require the session CSRF token in `X-CSRF-Token`:

- `GET /admin/settings/resend` returns the configured sender and readiness; it
  never returns the API key.
- `PUT /admin/settings/resend` accepts `apiKey`, `fromEmail`, and optional
  `fromName`. On later updates, `apiKey` may be omitted to keep the saved key.
- `POST /admin/settings/resend/test` accepts `{ "to": "staff@example.com" }`
  and sends a test message through Resend.

Staff create/verify the sending domain and API key in Resend, then enter the key
and verified sender address in HeyGo Admin. The HeyGo API key is encrypted at
rest. Resend delivery has not been live-tested; use the Admin test action after
configuring a real account.

## Driver data export

- `POST /driver/data-export` records one pending export for the authenticated
  Driver. It requires the signed CasperID business-token claims `email` and
  `email_verified: true`, private R2 storage, and saved Resend settings.
- `GET /driver/data-export/:exportId` returns only the owner-scoped request
  status. It does not expose the archive link through the API.
- A gateway worker builds a ZIP containing a JSON account/trip/earnings/ledger
  manifest and available private Driver documents/inspection reports. It stores
  the archive in the private R2 bucket and emails a seven-day presigned download
  link to the verified CasperID email.
- The worker retries using a stable delivery link and Resend idempotency key.
  After expiry it deletes the archive and clears the stored recipient/link.
  Failed archives are also queued for private-storage cleanup.

Configure the same private R2 bucket credentials used by Driver document APIs:
`R2_ENDPOINT`, `R2_BUCKET`, `R2_ACCESS_KEY_ID`, and `R2_SECRET_ACCESS_KEY`.
Export attachments are read server-side and are not made public.

## Account deletion review

- `POST /driver/account-deletion` records a pending request. `GET
  /driver/account-deletion` reads its current review status.
- Staff review the cursor-paginated queue at
  `GET /admin/account-deletion-requests?status=pending|approved|rejected|all`
  and decide with
  `POST /admin/account-deletion-requests/:requestId/review` using
  `{ "decision": "approve" }` or `{ "decision": "reject", "reason": "..." }`.
  Staff mutations require CSRF protection and are recorded in `admin_audit`.
- Approval is refused while the Driver has an assigned/accepted/arrived/started
  trip or an active offer. On approval HeyGo deactivates sign-in and online
  eligibility; replaces CasperID identifiers with non-personal IDs; clears
  profile, vehicle, safety-contact, device, location and onboarding document
  data; and queues private documents and report files for deletion from R2.
  Hashed identity tombstones prevent the same CasperID identity from silently
  recreating an account after deactivation.
- Completed trip and financial ledger records remain unchanged and retain their
  stable internal user ID for joins and accounting. The storage cleanup worker
  retries failed R2 deletions; Admin queue responses show outstanding cleanup.

The Driver and Admin frontends still need wiring to these APIs. Live CasperID,
Resend and R2 verification also remains pending.
