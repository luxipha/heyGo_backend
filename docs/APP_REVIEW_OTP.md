# App-review OTP and real email delivery

Updated 20 October 2026.

## Production email delivery

Resend credentials were saved through the production Admin API using its encrypted, write-only settings. Sender: `no-reply@sender.heygo.ng`, display name `HeyGo`. The deployed public `/auth/channels` reports email enabled/preferred and SMS disabled. An email sent through HeyGo's configured backend delivery-test endpoint was accepted; Resend subsequently reported `delivered` for the real recipient supplied by the owner. No API keys or OTP values are recorded here.

## Dedicated review account: implemented locally, not deployed

The new Admin **App review sign-in** panel configures one review email, a six-digit code, an expiry within 90 days and an enable switch. The chosen review identity is `dev_rider@heygo.ng`; it is a login identifier and has no receiving mailbox. Configure the code privately in Admin after deployment. Do not hardcode the code into mobile source or show it in the app.

This supersedes the earlier proposal to accept arbitrary test email addresses. The review code applies only to the single configured email. Other identities continue using random emailed OTPs through Resend. A review request uses the ordinary backend challenge, attempt limits, five-minute lifetime, account enrollment and real session issuance. Challenge responses never return the code. Review sessions and refresh tokens cannot outlive the configured expiry. Saving any settings change revokes existing review sessions and invalidates earlier review challenges. Settings reads never return the code; storage is encrypted with the existing Admin settings key. Writes use staff settings permission, CSRF, version checks and an audit reason.

The review account does not gain automatic driving eligibility, staff permissions, payment credits or role approval. Prepare its application data and ordinary onboarding/approval separately as required for store review.

Release requires migration `068_review_otp.sql`, API Gateway release and the Admin panel release. None of these review-OTP release steps or the review code activation have occurred yet. Production Resend configuration is independent and is already active.

## Validation

Isolated PostgreSQL tests passed for review challenge creation, correct identity scope, real authorized sessions, wrong codes, single-use challenges, refresh expiry bounds, disabling/revocation, stale-write rejection, audit records and unchanged normal provider delivery. Existing email OTP enrollment/session and Admin SMS tests also passed. The temporary test database was removed. Admin typecheck/build and all 56 Admin tests passed. Driver analysis and targeted authentication/responsive tests passed; the demo banner and sign-in helper line were removed and checked on the simulator.

Actual backend testing must run the app with the deployed API URL and without `DEV_BYPASS_AUTH` or `DEV_DEMO_OTP`. Those older debug switches still enter a local preview; they do not test server authentication.
