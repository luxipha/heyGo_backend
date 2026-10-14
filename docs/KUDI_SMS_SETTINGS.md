# Kudi SMS and authentication-channel availability

Implemented 2026-10-14 (Asia/Ho_Chi_Minh). SMS delivery uses Kudi, configured in Admin; SMS is disabled until configured and explicitly enabled. Resend/SMTP email remains independently controlled.

## Provider contract

User-supplied [Postman collection](https://documenter.getpostman.com/view/44181644/2sB2cd3HUd) and official [Kudi SMS docs](https://www.kudisms.net/docs/sms/) were inspected. The user's linked `Send OTP` operation (`/api/sendotp`) generates/verifies provider-owned codes. The same collection's **Send SMS OTP** (`POST https://my.kudisms.net/api/otp`) delivers a HeyGo-generated code. This implementation uses the latter so existing HeyGo challenge hashes, five-minute expiry, five attempts, single-use verification, enrollment and phone-change proofs stay authoritative.

Request is multipart form-data: `token`, `senderID`, `recipients`, `otp`, `appnamecode`, `templatecode`. A Nigerian `+234` recipient is sent as `234…` without the plus. There is no bearer header or query-string credential. Credentials, codes and raw provider responses are never returned to Rider, published as telemetry, or added to audit details. Acceptance requires HTTP 2xx **and** `status: success` / `error_code: 000`; HTTP 200 with a provider error is rejected. Acceptance is not handset delivery. Unknown/malformed/lost responses remain unconfirmed; the adapter does not automatically retry or switch channels.

## Admin configuration

The existing Settings area now adds **Kudi SMS delivery**, using existing section/field primitives. Configure:

1. Kudi API key (encrypted, write-only; blank on save preserves the existing key).
2. Approved **Corporate Sender ID**, up to 11 characters.
3. Approved **App Name Code**.
4. Approved **SMS OTP Template Code**. Its wording must match the actual HeyGo five-minute expiry.
5. Enable or disable SMS OTP delivery.

A disabled configuration can be saved before a live account is ready. Enabling requires all fields and a saved key. Saving does not send a real SMS or establish that Kudi approved the identifiers. The existing 32-byte `HEYGO_ADMIN_SETTINGS_KEY` encrypts the API key with a distinct Kudi secret purpose. GET/PUT `/admin/settings/sms` require the existing settings permission; PUT requires CSRF and records a safe staff audit event. No SMS test-send action or delivery webhook was added; use a controlled real Rider OTP journey after configuration.

Migration **063_kudi_sms.sql** adds the singleton configuration. Generic `SMS_PROVIDER_URL`, `SMS_PROVIDER_TOKEN`, `SMS_SENDER_ID` environment variables no longer select the gateway's production sender. Main gateway auth and Rider phone-change verification both use the persisted Kudi sender. Test/staging injected delivery recorders remain controlled test adapters, not production fallback providers. This backend change also governs phone OTP for Driver accounts; Driver UI was not changed.

## Rider behavior

Public `GET /auth/channels` returns only `email.enabled`, `sms.enabled`, `preferredChannel` (`email`, then `phone`, or empty), and `scope: new_otp_requests`. It is separately rate-limited and returns `Cache-Control: no-store`; it does not expose provider names, hostnames, sender identity, templates, credentials, balances or delivery diagnostics. Availability means enabled/configured/decryptable with OTP pepper configured, not live provider health. Database-read failures return a distinguishable unavailable error rather than invented enabled states.

Mounted Agent onboarding reads availability on entry and before requesting an OTP. Email remains prioritized. Existing sign-in choices remain present, with unavailable choices disabled; explanatory states offer the enabled alternate method and a refresh action. If both methods are disabled or the read fails, neither method can submit. The OTP request handlers also enforce readiness, and sender adapters re-read saved controls before sending, so stale clients cannot bypass an Admin disable. A send already in flight cannot be recalled.

An already-issued valid proof can still be verified after disabling delivery; disabling stops **new sends**, not revocation of all sessions/challenges. Phone changes continue to require SMS proof of both current/new phones; email cannot substitute for phone ownership.

Alternate-channel sign-in is an explicit Rider choice with entered contact details. It never automatically transfers an email challenge to a phone, derives an unknown phone, or merges identities. A returning Rider should use an already-verified contact linked to their existing account; an unrelated contact must not be presented as account recovery.

## Verification and remaining gates

- Automated Kudi adapter tests inspect exact fields/auth/recipient formatting and reject HTTP-success provider failures/malformed responses/foreign numbers.
- Database-backed tests cover migration, disabled default, configuration requirements, encryption, write-only reads, password/key preservation, public availability privacy and disabled-new-request enforcement.
- Rider tests cover malformed DTOs, email-disabled/SMS-enabled, SMS-disabled/email-enabled, both-disabled, unknown availability and disable during entry before send. Existing layout/flow tests remain in the suite.
- Admin API tests cover persisted settings endpoints, CSRF and write-only credentials.
- [ ] Apply migrations 062/063 to the intended environment before deploying gateway/Admin/Rider changes.
- [ ] Configure approved Kudi credentials/identifiers through Admin; verify real +234 OTP receipt and actual HeyGo verification.
- [ ] Exercise enabled/disabled email/SMS combinations and stale client entry on authenticated installed devices.
- [ ] Inspect the authenticated Admin settings panel visually. Full Admin typecheck remains blocked by the existing missing PartnerTripsTab import.
- [ ] Review stale preview-only OTP copy in `assets/onboarding/onboarding_script.json` as a preserved-UI wording task; its preview claims are not authentication truth, and no fixed code is accepted by the backend.

Executed 2026-10-14: full gateway handler suite and gateway package checks passed against disposable PostGIS (12.151s); full Rider suite **87 tests passed**; Rider analyzer **No issues found**; Admin API-client suite **15 tests passed**. Scoped whitespace checks passed. No native/device visual fidelity or provider delivery proof is claimed. Logs: `/private/tmp/heygo-kudi-backend-final.log`, `/private/tmp/heygo-kudi-rider-all-final.log`, `/private/tmp/heygo-kudi-rider-analyze-final.log`. Disposable database server stopped after verification.
