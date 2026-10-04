# Admin launch money rules

Status: **launch scope confirmed; not implemented or enabled**. The Admin UI would create and review rule versions; the backend would validate, approve, select, and enforce the effective version. A browser-only setting must never authorize a money movement.

## Current money paths

| Source | Current backend behavior | Launch handling |
| --- | --- | --- |
| Driver Operating Balance top-up | Monnify verifies the payment before an append-only `topup` entry credits the Driver account; that balance can later fund statutory charges and no-show transfers. | Refund of the original, verified Monnify payment, with a corresponding source-linked ledger reversal only after provider confirmation. |
| Rider no-show debt payment | Monnify verifies a Rider payment, settles the debt, and credits the claimant Driver's Operating Balance. | Requires reversing the claimant credit and debt state as one governed operation; **outside first launch** because the existing settlement can have downstream allocations. |
| Ordinary Trip fare | Rider pays Driver directly. The backend records a Driver assertion, not a HeyGo-collected transaction. | Support may handle a dispute, but HeyGo cannot issue a Monnify refund of this fare. Any compensation would be a separate funded product decision. |
| No-show claim | Finance already approves or rejects a claim under a versioned no-show policy. | Keep the existing decision flow and audit. |
| Operating Balance, statutory, no-show policy | Existing Admin routes create drafts and require a different staff account to approve. | Keep versioned, effective-dated, backend-enforced rules. |

## Confirmed first money action

A Finance staff member may propose a **full refund of one successful Driver top-up** tied to its Monnify transaction. A different authorized staff member (Finance or Super Admin) must approve it. The backend must verify the original transaction, reject a duplicate or excess refund, confirm that the Driver's available Operating Balance can cover the debit without going negative, and record the reason, case ID, actors, timestamps, rule version, provider reference, and idempotency key. Approval creates a durable pending refund request and reserves the refundable balance under the account lock; every competing charge or transfer must use the balance **after reservations**. Approval does not imply that Monnify has paid the customer. A worker sends the request and reconciles terminal success or failure from a signed webhook and status lookup. Confirmed success atomically consumes the reservation and appends the source-linked debit; confirmed failure releases it. An uncertain refund keeps the reservation and remains visibly unresolved for Finance. This operation stays disabled until Monnify enables refunds for the merchant, confirms that the original payment method is refundable, and the required destination-account behavior is verified. Monnify currently documents a ₦100 minimum, a transfer-refund fee deducted from the merchant wallet, and an account-transfer-only restriction in its error guidance; the backend must check the provider contract before enabling execution.

The following are **outside the first action**: a free-form balance credit/debit, a refund of a direct Rider-to-Driver fare, a paid no-show debt refund, or a cash payout. A Support case status change never moves money.

## Admin-configurable policy

Finance drafts a version with an effective period, allowed reason codes, per-request and daily maximums in integer kobo. The first version permits only full refunds of verified Driver top-ups. A different Finance or Super Admin account approves it. The backend rejects overlapping active versions and applies the version effective when a request is made. A new version cannot rewrite earlier decisions. The initial state is disabled until an approved version and provider readiness both exist.

The following controls are fixed backend invariants, not editable toggles: Finance or Super Admin permission and CSRF; proposer and approver differ; source is a verified, succeeded HeyGo-collected payment; cumulative approved/completed refunds cannot exceed the original payment; the reservation prevents intervening spending; a stable idempotency key prevents double sending; ledger entries remain append-only; a completed refund requires provider evidence; and every transition has an audit receipt.

For a virtual-account or other account-transfer refund, the authenticated Driver supplies a regular bank destination for that specific request. The backend uses Monnify Name Enquiry, encrypts the account number at rest, and shows only a masked number to staff. Compliance records the Driver's legal name from an approved identity document with reviewer and time; the bank account name must match that reviewed name. A mismatch holds the refund until Compliance rechecks the identity evidence or the Driver supplies another account. Existing Drivers without a reviewed legal name cannot receive a refund. A Support case outcome does not override this gate.

The daily policy ceiling uses the **Africa/Lagos calendar day**. Provider execution is disabled unless `HEYGO_MONNIFY_REFUNDS_READY=true`; the checked-in Cloud Run and development manifests set it to `false`. Keep it off until controlled sandbox and live tests confirm this merchant's activation, original-payment method, regular-bank destination, fee, signed terminal webhook, and status lookup. A claimed send is never sent a second time automatically: if its response is lost, the reservation stays in `needs_reconciliation` until provider status resolves it.

## Planned workflow and routes

1. `GET /admin/finance/refund-policy` shows effective and pending versions. `POST /admin/finance/refund-policy` drafts a version; `POST /admin/finance/refund-policy/:id/approve` uses a different Finance or Super Admin actor.
2. `POST /admin/finance/refunds` proposes a source-linked refund with reason and Support case ID. `GET /admin/finance/refunds` and `GET /admin/finance/refunds/:id` show status and evidence. `POST /admin/finance/refunds/:id/approve` requires a different Finance or Super Admin actor.
3. The backend records `draft -> approved -> provider_pending -> completed | failed | needs_reconciliation`. Retrying or receiving duplicate provider events must be idempotent. A reversal is appended only after provider completion and is linked to the original credit and refund ID.
4. Support owns the case conversation; Finance owns the refund decision. Super Admin may propose or approve the action, but never both on the same request. The two-person approval still applies.

## Launch prerequisites

- Finance configures reason codes and per-request and daily kobo ceilings in Admin. No numeric ceiling is hard-coded, and the feature remains disabled until a second authorized staff account approves the version.
- Monnify enables the refund API for the merchant in sandbox and live. Verify the payment method, provider fee, refund reference, amount conversion, terminal webhook, and status lookup against real provider behavior before enabling execution.
- HeyGo funds Monnify's transfer-refund fee from its merchant wallet and records it as an operational cost. The Driver's Operating Balance is debited only for the refunded top-up amount.
- When the original payment used a virtual account, collect a regular bank destination through a secure, request-scoped flow. Do not return full account details in broad Admin lists or logs.
- Configure two distinct authorized staff accounts. Super Admin can perform any Finance action, but cannot approve the same request they proposed. With one Super Admin and one other staff member, that second person needs Finance permission for the approval flow.

Provider reference: [Monnify Transaction Refunds](https://developers.monnify.com/docs/collections/manage-payments/refunds). Monnify documents separate activation for the refund API, full/partial refunds, terminal webhooks, and destination-account constraints. Provider availability and merchant configuration require live verification.
