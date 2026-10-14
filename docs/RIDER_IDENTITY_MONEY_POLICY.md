# Rider identity and launch payment policy

Approved in this session on 2026-10-12. Source and automated verification are distinct from provider/device evidence.

## Identity ownership and correction

- A Rider display name is self-managed, optional until first saved in Personal details, and stored in `rider_profiles`. It is not a verified legal name. `GET /auth/me` exposes `riderDisplayName` from this source for Rider accounts, including dual-role accounts. Its generic `name` field retains the Driver source for Driver/dual-role compatibility and uses the Rider source for Rider-only accounts; the Rider app explicitly prefers `riderDisplayName`. There is no automatic copy from Driver/Firebase/local fixtures or upstream identity text.
- `GET /rider/profile` reads the owned name and declares `nameKind: display`; `PATCH /rider/profile` accepts only `displayName` (1–100 trimmed characters, no control characters). Saving is account-locked and persisted before acknowledgment. The app rereads `/auth/me`, so Home/Profile use the saved source.
- Legal-name corrections go through owned Support cases. Support owns intake and staff review; identity evidence must use the existing private attachment process, not Agent text/telemetry or public payloads. Staff record the requested correction, evidence checked, reviewer and outcome in the case. A case acknowledgment/closure, self-edited display name, or notification does not verify a legal identity or change provider trust. Where a verified provider identity needs correction, its authoritative verification process must confirm that correction. The Rider entry opens the existing owned Support workflow; no Driver mutation or Rider-callable Admin route is reused.
- Phone changes require a verified current phone, a different Nigerian `+234` number, proof of both numbers, and a live owned session. Missing/lost current-phone access uses staff-reviewed Support recovery; this release does not bypass the current-phone proof or invent an email-only first-phone enrollment rule.
- Email editing uses a live owned session and proof of the proposed email. It does not merge accounts. An email held by another account or a deactivation tombstone is unavailable. Changes remain unapplied until verification. The legacy `/auth/account` route cannot assign a new email to a Rider without verification; Driver-only behavior is preserved. Public email OTP sign-in does not silently link an unverified email alias to an existing Rider account; those legacy accounts must use owned linking/review.

## Contact contracts

| Operation | Request | Acknowledgment / result |
| --- | --- | --- |
| `POST /rider/identity/phone-change/request` | `{destination: "+234…"}` | HTTP 202: challenge ID, actual expiry, current/new destinations and `requiresCurrentCode: true`; no identity change. |
| `POST /rider/identity/phone-change/confirm` | `{challengeId, currentCode, newCode}` | Both proofs apply the phone change and revoke **all** account sessions atomically; `{updated: true, verified: true, sessionsRevoked: true}`. The app clears local credentials and returns to sign-in. |
| `POST /rider/identity/email-link/request` | `{destination: "…"}` | HTTP 202: owned challenge and actual expiry; the email is not yet linked. |
| `POST /rider/identity/email-link/confirm` | `{challengeId, newCode}` | Persisted verified email; app rereads identity. Existing phone/contact changes invalidate outstanding proofs. |

Proofs expire after five minutes, have five maximum failed attempts, and are limited to three requests per account per ten minutes. Codes are HMAC-hashed and bound to the account-owned challenge, purpose, destination and current/new leg. Sign-in OTPs cannot be reused for these operations. Confirmation checks current stored identity, deactivation tombstones and unique ownership again; simultaneous phone confirmations have one winner. Bad/expired/foreign/consumed proofs return 422 `invalid_otp`, not a session-expiry signal. Unavailable delivery, contact collision, changed identity and lost authentication have separate errors. A delivery failure invalidates the proof without applying a mutation. Neither request nor response logging should include codes, tokens or bodies.

If a successful phone-change response is lost, its old sessions remain revoked. Sign in using the new number and recover `/auth/me`; an unobserved response must not be treated as proof that the old phone remains active. Rider privacy export uses the Rider name; reviewed account-wide deletion removes the profile/contact challenges and preserves the existing financial retention policy.

## Approved J-21 launch scope

1. Ordinary fares are paid directly to the Driver. The existing settlement read/receipt reports pending, confirmed or disputed **Driver assertions**. These are not provider-verified payments collected by HeyGo.
2. A no-show debt requires the existing wait/claim/Finance-approval flow. `GET /rider/no-show-debts` returns approved amounts due in integer Kobo/NGN. Debt is neither a wallet balance nor an automatic cancellation fee.
3. `POST /rider/no-show-debts/:id/pay` requires a verified account email and stable `Idempotency-Key`. The gateway uses the authenticated owner; the payment service rechecks the active Rider and persisted verified email under a shared account lock. Checkout amount and ownership come from the approved debt, not Flutter input. Existing atomic debt/idempotency/one-pending-checkout rules remain in place.
4. `GET /rider/no-show-debt-payments/:id` recovers an owned provider payment. Added `GET /rider/no-show-debt-payments?cursor=…` returns 30 owned entries with keyset pagination and `nextCursor`; it is **debt payment activity**, not a generic wallet ledger. `pending`, `succeeded` and `failed` remain distinct. Succeeded means the backend has verified the provider transaction and posted the linked settlement once.
5. Provider creation errors remain pending/unresolved because a lost response cannot prove that checkout creation failed. A retry with the same key recovers the original record, including a null URL while unresolved, and does not invoke provider creation again. Operators must reconcile the server-owned provider reference before releasing/replacing an uncertain attempt. No automatic new attempt, refund or debt-clearing action is inferred from a browser return.

The new Rider debt page rereads amounts after explicit confirmation, retains its idempotency key in secure storage across controller/app restart and refresh retries, and obtains provider URLs/status from owned reads. It opens only an HTTPS backend-issued checkout URL in the external browser; resume refreshes authoritative debt/payment status. Unknown outcomes stay pending/unavailable. A confirmed failed **latest** attempt can retire its local retry key; older failed records cannot retire a newer pending attempt's key. Card/merchant secrets never enter this flow.

Saved cards, stored-money wallets, generic instruments/ledger/balance/activity, and HeyGo collection of ordinary fares are **deferred by explicit user approval**. No ordinary-trip `PaymentCmdCreateSession` producer is added. Existing card form visuals remain preserved, but fields are read-only so deferred controls cannot collect cardholder/PAN/expiry/CVV data. Their unavailable save action remains in place. The external browser uses Flutter's [url_launcher](https://pub.dev/packages/url_launcher); compatible package versions retain the existing native minimum while the separately documented Android toolchain upgrade remains open.

## Disputes and refunds

The [Admin launch money rules](ADMIN_LAUNCH_MONEY_RULES.md) remain authoritative. Direct-fare disputes use trip-linked Support and existing direct-settlement evidence; HeyGo cannot refund a Driver-collected fare through Monnify. Paid no-show-debt refunds are outside the first launch because claimant credits and downstream allocations require a governed reversal. Support acknowledgment/closure never moves money. No compensation, cash payout, automatic fee, balance credit or generic Rider refund capability is introduced by this integration.

## Preserved UI and remaining evidence

The user approved only these old action wording changes: `End trip` → `Refresh trip status`; `Payment sent` → `Check payment status`. Defaults, canonical bindings and the preserved Home callbacks now use authoritative refresh/recovery, with those labels and existing layouts. Personal-details and debt controls use the existing Rider scaffold/tokens. Legal-review entry carries explicit review context into the owned Support composer and omits an unrelated trip selector; other Support defaults are preserved. Map illustrations and geometry visuals have not been redesigned.

- [ ] Compare authenticated preserved/new widgets on installed Android/iOS devices at approved viewports; include Classic/Driver illustrations versus authoritative geometry and record any old-layout changes before redesign.
- [ ] Review remaining preserved wallet/card explanatory wording for the explicitly deferred product scope before changing those labels/layouts.
- [ ] Resolve the preserved generic Support “Link most recent ride” fixture/toggle through an approved real-history selector and explicit persisted context handoff; the new legal-review entry does not use this selector.
- [ ] Observe actual current/new phone and email delivery through configured providers; controlled local tests intercept codes.
- [ ] Observe real no-show checkout, browser return/resume, webhook/status verification and operational reconciliation of unresolved creation against a controlled provider target.
- [ ] Observe staff handling of an actual legal-name review/private evidence and any authoritative provider correction. A Support intake link is not completed identity verification.

See [RIDER_VERIFICATION_EVIDENCE.md](RIDER_VERIFICATION_EVIDENCE.md) for automated, controlled local staging and native evidence. Migration 061 adds Rider profiles/contact challenges; migration 060 is unchanged.
