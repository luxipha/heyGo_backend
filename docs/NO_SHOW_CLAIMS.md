# No-show claims and rider debt

This backend flow implements the approved HeyGo rule:

1. Driver B marks arrival through `POST /trips/:tripID/arrival`. The server stores the arrival time.
2. The active market policy sets the wait period and fee. After the wait, Driver B submits a claim. The backend ends that trip as a no-show candidate and records the policy, arrival, eligibility time, rider, and fee snapshot.
3. A HeyGo staff member reviews the claim. Rejection ends the claim without charging the rider. Approval creates an outstanding rider debt; it does not credit either driver's Operating Balance yet.
4. Rider A can read debts at `GET /rider/no-show-debts` and pay an approved debt directly to HeyGo using `POST /rider/no-show-debts/:id/pay`. The response provides a Monnify checkout. Only a signed webhook followed by server-side Monnify transaction verification can settle the debt and credit the original claimant's Operating Balance. The rider can also leave a debt unpaid; `POST /trip/preview` adds outstanding debt to a future fare, and the snapshot travels with the trip and offer.
5. For a future ride, the collecting driver's manual settlement confirmation covers fare plus allocated debt. If their Operating Balance can fund the transfer, one transaction debits that driver, credits the original claimant, settles the debt, and emits events. If it cannot, the trip settlement still completes; the collected debt is marked `collection_pending`, excluded from future rider charges, and its transfer is retried after the collecting driver receives an Operating Balance top-up.

## Endpoints

| Role | Endpoint | Purpose |
| --- | --- | --- |
| Driver | `GET /driver/trips/:tripID/no-show` | Check eligibility and see the server arrival time, eligible time, wait period, and fee |
| Driver | `POST /driver/trips/:tripID/no-show-claims` | Submit an eligible claim; idempotency key supported |
| Driver | `GET /driver/no-show-claims` | Read the driver's claims and resulting rider-debt status |
| Rider | `GET /rider/no-show-debts` | Read outstanding fees and total due |
| Rider | `POST /rider/no-show-debts/:id/pay` | Create/replay a provider checkout for a due debt; requires `Idempotency-Key` |
| Rider | `GET /rider/no-show-debt-payments/:id` | Read the rider-owned payment's pending/confirmed status |
| Admin | `GET /admin/no-show/policies` | List market policy versions |
| Admin | `POST /admin/no-show/policies` | Create a draft wait-period and fee policy |
| Admin | `POST /admin/no-show/policies/:id/approve` | Approve a policy with a second staff account |
| Admin | `POST /admin/no-show/policies/:id/retirement-requests` | Request an effective end date for an approved policy |
| Admin | `POST /admin/no-show/policies/:id/retirement-requests/:requestID/approve` | Approve policy retirement with a second staff account |
| Admin | `GET /admin/no-show/claims?status=pending` | Review submitted claims |
| Admin | `POST /admin/no-show/claims/:id/review` | Manually approve or reject a claim, with an optional note |

Money fields are integer kobo. A policy is inactive until approved, and approved values are versioned rather than edited in place. No fee or wait-period default is seeded.

## Arrival evidence and limits

Eligibility uses the server-recorded `arrived_at` and the approved market wait period. This implementation does not require a GPS radius check. Admin sees the server arrival/eligible timestamps and trip/rider/driver IDs. Those timestamps establish that the configured wait elapsed after the driver marked arrival; they do not prove the rider was absent.

The settlement transfer obeys the collecting driver's approved market `allow_negative` and `maximum_negative_kobo` settings. If the available balance cannot fund a future-trip transfer, the completed trip is not rolled back and the rider is not charged twice. The debt remains in `collection_pending` while the Operating Balance transfer retries after top-up. No money moves on claim submission or Admin approval.

Direct rider payments are credited only after Monnify signature validation and a server-to-server transaction query confirms the exact amount, NGN currency, payment reference, and transaction reference. Webhook replay is safe: the debt and claimant credit are posted atomically and at most once.

Migrations: `shared/db/migrations/021_no_show_claims_and_debt.sql` and `shared/db/migrations/022_rider_no_show_debt_payments.sql`.
