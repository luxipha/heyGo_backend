# Hero → Driver referral stages

Admin configures staged rewards in **Settings → Referrals → Hero → Driver**. Enabling “Pay rewards in stages” loads the 10/30/50/70-trip template with ₦7,000/₦10,000/₦15,000/₦20,000 incremental rewards (₦52,000 total). Saving captures a new settings version; it does not enable referrals automatically or rewrite existing attributions.

Each stage independently chooses manual or automatic wallet approval and manual bank payment or Monnify transfer after approval. Trip thresholds are cumulative since referral linking. Registration, current Driver eligibility, real completed trips, the captured deadline, and simulation/self-trip exclusions are enforced by the backend. Manual approval cannot qualify an unfinished stage.

## Finance and Heroes

- **Finance → Referral rewards → Hero → Driver stages** shows qualification, approval mode, credited amount and payout status per driver/stage. Finance permission is required for approval, payout requests, OTP authorization/resend, and cancellation.
- **Finance → Heroes payments** records completed manual bank transfers. Select the credited, unpaid stages included in the transfer; the amount must equal those rewards. A transfer can cover several whole stages. Existing single-reward payments can still be recorded without stage allocations, up to the remaining legacy earnings.
- Monnify may also be requested manually for a credited stage. The automatic worker queues only stages whose captured payment mode is Monnify. A queued transfer can be cancelled before submission to permit manual payment. Pending, ambiguous or OTP-authorized transfers reserve their stage against a second payment.
- Heroes shows each driver's stage timeline, qualifying trips, reward, approval/payment state, credited/paid totals and remaining unpaid earnings. Its trip counting uses the same captured deadline as Finance qualification. Refreshing also updates an open driver detail sheet.
- A manual record correction reopens the allocated stages without deleting history. A provider-paid transfer cannot be corrected manually; only a provider-confirmed reversal reopens that payment.

## Monnify setup and execution

Apply migration **129** before deploying the gateway and payment-service changes. Both services need their existing internal authentication and encrypted-settings configuration. Configure the Monnify disbursement wallet account in **Settings → Integrations → Monnify payments**, along with the existing account credentials. The wallet number is stored encrypted, never returned to browsers, and pinned through the payout's captured Monnify settings version. Existing sandbox/live restrictions are unchanged.

Heroes must select a bank and save a Monnify-verified account for automatic payouts. Previously saved manual bank details continue to work for manual transfers. Unavailable bank verification does not mark an account verified.

Gateway maintenance invokes the payout worker in both pull-mode jobs and the authenticated push maintenance endpoint. The worker commits a payout reservation before submitting once with a persistent reference. A lost response moves to reconciliation, never a second initiation with a new reference. Only a status query matching reference, amount, NGN currency, bank and account can settle the stage. Wallet credit and bank payment remain distinct.

OTP statuses stay unpaid until authorization and confirmed settlement. Finance can submit or resend the Monnify OTP. Failed, expired or reversed transfers require an explicit Finance retry; automatic maintenance does not repeatedly create new transfer references. An uncertain transfer must be reconciled with Monnify before a manual replacement is allowed. Successful transfers continue to be polled for reversals.

Provider contract: [Monnify single transfers](https://developers.monnify.com/docs/disbursements/single-transfers).

## Verification

- `go test ./shared/referrals ./services/api-gateway/handler ./services/payment-service/service ./services/payment-service/handler`
- Database tests require a **disposable** migrated PostgreSQL database via `HEROES_TEST_DATABASE_URL`, `REFERRAL_TEST_DATABASE_URL`, and `MONNIFY_TEST_DATABASE_URL`. They create fixtures and settings versions; do not point these variables at application or production databases.
- `npm run typecheck` and `npm run build` in Admin; `npm run build` and `npm test` in Heroes.
- `TestHeroStagesQualificationPaymentsAndProviderReconciliation` covers immutable snapshots, mixed approval modes, excluded/deadline trips, concurrent approval retries, manual allocation/reversal, queued transfer cancellation, ambiguous provider submission, pending authorization, confirmed settlement and provider reversal. Provider HTTP calls are mocked.

No live transfer, remote migration, deployment, or migration of existing referral contracts is part of local implementation verification.

## Rank qualification

Migration **131** adds a separate completed-trip requirement for every Hero rank. Admin configures **Settings → Heroes** as one row per rank: **required drivers** and **completed trips per driver**. Rank E starts at zero. Driver counts increase toward A; trip requirements can stay equal or increase. For example, D can require 5 approved referred drivers with 10 trips each, while C requires 10 drivers with 30 trips each. Every driver is counted at most once toward each rank.

Existing policy versions retain the shared minimum introduced by migration 130 until a new version is saved with `rankTripRequirements`. Immutable referral reward snapshots and payment states are unaffected. Real completed trips count since linking, excluding self/simulation trips and respecting any captured referral deadline.

`/heroes/me` returns `rankRequirements` with an independent qualifying-driver count for each rank. `nextRank` includes that rank's trip requirement, qualifying count and remaining drivers. Rank progress never reuses the count for a different trip threshold. The leaderboard orders by achieved rank, then progress toward the next rank; it includes both legacy and programme referrals.

`TestHeroRanksCountDriversMeetingTripMinimum` verifies legacy compatibility, per-rank persistence/cohorts, changing one rank without changing another, next-rank and leaderboard consistency, deadline exclusion, and unchanged onboarding/reward values.
