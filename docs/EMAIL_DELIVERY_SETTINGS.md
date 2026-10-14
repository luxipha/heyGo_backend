# Email delivery settings

Implemented 2026-10-14 (Asia/Ho_Chi_Minh). Resend is the default email provider. SMTP is an optional email fallback, not an SMS fallback. No live SMTP server or provider credentials were provisioned.

## Admin controls

Settings retains the existing Resend sender/API-key panel and adds **Email provider routing**. Configure SMTP host, port, required STARTTLS (normally 587) or implicit TLS (normally 465), username, write-only password and sender. TLS certificate and hostname verification are mandatory; plaintext mode is unavailable. Authenticated SMTP currently supports PLAIN authentication over verified TLS; an unauthenticated TLS relay can omit username/password.

- Enable/disable Resend and SMTP independently.
- Choose Resend or SMTP as primary.
- Enable/disable fallback to the other enabled provider.
- Turning off both providers stops Rider OTP/email-link and staff access email delivery.
- Blank SMTP password on save preserves the encrypted existing password. Passwords and Resend keys are never returned. Both require the existing 32-byte `HEYGO_ADMIN_SETTINGS_KEY` encryption secret.
- A disabled primary is skipped only if fallback is enabled. Otherwise delivery reports unavailable. SMTP may remain disabled while its future configuration is saved.
- Test uses saved provider routing; edits must be saved first. Acceptance is not proof of inbox delivery.

No settings row preserves existing behavior: Resend enabled, primary Resend, SMTP and automatic fallback disabled. Existing Resend credentials remain in `admin_resend_settings`; migration **062_email_delivery.sql** adds only provider routing/SMTP configuration.

## Ownership and APIs

The authenticated Admin settings permission protects GET/PUT `/admin/settings/email-delivery` and POST `/admin/settings/email-delivery/test`. Mutations require the existing CSRF token. Saved provider changes and accepted tests write staff audit records; secrets are excluded. SMTP endpoints are privileged deployment configuration, not Rider-controlled input.

GET/PUT return `settings`, `smtpConfigured`, `passwordConfigured`, `encryptionReady`. Password and ciphertext are absent. PUT fields: `resendEnabled`, `smtpEnabled`, `primaryProvider`, `fallbackEnabled`, `smtpHost`, `smtpPort`, `smtpTlsMode`, `smtpUsername`, optional `smtpPassword`, `smtpFromEmail`, `smtpFromName`. POST test body: `{ "to": "recipient@example.test" }`; success returns `{ "accepted": true }`.

## Consumer wiring and fallback semantics

Public email sign-in OTP, Rider authenticated email-link OTP and staff invitation/recovery/verification share the configured sender. Changing the saved route takes effect without rebuilding Rider or restarting the gateway. OTP identities, expiry, attempts, enrollment, verification and session rules remain backend-owned.

Fallback is attempted only on definite non-acceptance: disabled/missing configured primary, an explicit Resend transient rejection (429 or 5xx), or SMTP failure before message DATA. Resend connection/response-loss outcomes do not trigger fallback. SMTP DATA write/response-loss outcomes remain unresolved and do not trigger another send. Accepted SMTP DATA remains accepted even if QUIT fails. Both providers receive the same OTP; no alternative code is generated during fallback. These synchronous OTP sends do not introduce a durable retry queue or delivery webhook/inbox-status contract.

**Boundary:** privacy archive emails retain their existing Resend-specific sender and idempotent export job. The routing form explicitly states this; these controls do not govern export mail yet. Extending privacy exports to SMTP requires preserving export retry/uncertain-acceptance behavior and is a separate task. Generic Notification Settings toggles remain local UI drafts and are not email provider controls.

## Verification

- Automated tests cover provider selection/disable/fallback, definite versus uncertain acceptance, SMTP required STARTTLS and implicit TLS against controlled local SMTP servers, SMTP encrypted-secret purpose isolation, persisted migration/defaults/write-only password/blank-password preservation and settings permission mapping.
- Admin API tests verify saved routing paths, CSRF and the write-only password contract.
- Full Admin typecheck currently encounters the pre-existing missing `components/partners/PartnerTripsTab` import. A successful focused API test does not establish full Admin compilation or visual verification.
- Live Resend inbox receipt, real SMTP delivery/failover and authenticated Admin visual checks remain open. No real OTP/test email was sent during implementation.

## Remaining tasks

- [ ] Configure a verified Resend sender and test recipient in the intended environment; observe real Rider OTP receipt/verification.
- [ ] When an SMTP server is available, enter its configuration through Admin and verify primary/fallback/disabled behavior without production disruption.
- [ ] Visually verify the authenticated Admin routing panel and persisted read-back.
- [ ] Restore/resolve the unrelated missing PartnerTripsTab before claiming full Admin typecheck/build success.
- [ ] If SMTP must cover private exports, extend the export worker and readiness checks with durable acceptance/retry semantics before enabling that scope.
- [ ] Apply migration 062 to the intended gateway database before deploying the new routing controls; this run migrated only a disposable test database.

Executed evidence (2026-10-14): focused database-backed gateway/Rider authentication checks passed; controlled SMTP TLS/STARTTLS acceptance and response-loss tests passed; all 14 Admin API-client tests passed. No provider/live or authenticated visual proof is claimed.

The full gateway handler suite also passed against the disposable PostGIS database (16.181s); output: `/private/tmp/heygo-email-backend-tests.log`. The disposable PostgreSQL server was stopped after verification; its test data remains under `/private/tmp/heygo-email-pg`.
