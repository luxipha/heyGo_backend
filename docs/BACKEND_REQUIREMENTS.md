# Admin backend requirements checklist

Source review updated 1 October 2026. A checked item means implemented in backend source, not deployed, connected to the Admin frontend, or verified with live providers. Both checkouts contain unrelated uncommitted work. This checklist does not claim production readiness.

Backend: `/Users/abisoye/Projects/heyGo/backend`  
Admin: `/Users/abisoye/Projects/heyGo/apps/admin`

## P0 — foundation for connecting Admin

### Staff access, roles, and audit

- [x] Staff login, `/admin/auth/me`, logout, HttpOnly session cookie, and CSRF protection.
- [x] Enforce route permissions for Operations, Compliance, Support, Finance, Safety, and Super Admin. `/admin/auth/me` returns primary `role`, all `roles`, and combined permissions. Super Admin has every domain permission.
- [x] Support multiple roles on one staff account; revoke sessions when roles or active state change. Staff list, detail, and update routes exist. Proposal and approval of the same governed change require different staff IDs.
- [x] Provide cursor-paginated staff audit list/detail for event metadata, restricted to Super Admin.
- [x] Add Super Admin email invitation, resending, verified staff email, and single-use password recovery. The CLI remains the fallback if both staff lose access. Live email delivery requires configured Resend settings and `ADMIN_FRONTEND_URL`.
- [x] Apply current staff roles across the launch market with no team hierarchy. Per-market grants are deferred until staff actually manage multiple markets.
- [x] Give Compliance and Super Admin Driver contact permission; give Support and Super Admin a case-linked contact endpoint. Operations, Finance, and Safety have no contact permission through these routes.
- [x] Add typed audit targets, operator reasons, and a redacted before/after projection to staff audit reads; record staff access changes and case-contact reads without putting email or phone values in the staff-facing diff. Role and activation changes require a reason.
- [ ] Complete before/after coverage for every Admin mutation and verify audit retention/immutability controls in the deployed database. Older events and some domain actions still have metadata-only evidence; do not call this history tamper-proof.

### Trips, Riders, and live operations

- [x] Provide staff Trip and Rider list/detail routes with stable IDs, cursor pagination on lists, Trip status and party-ID filters, integer-kobo fare, and no broad contact or raw route disclosure.
- [x] Provide `/admin/overview` counts and `/admin/live/drivers` polling with `asOf`, location `recordedAt`, a 10-second refresh hint, and current/stale/disconnected presence. The live list omits contacts and vehicle plates.
- [x] Add safe Trip detail links for persisted lifecycle milestones and assignment attempts, current vehicle profile, quoted fare and market classification, quoted route distance/duration, Driver-asserted settlement, ratings, no-show claims/policy, and permissioned Support, statutory-charge, and debt-payment states. The broad response omits route geometry and contact data. Existing Trip detail fields remain at the top level, with `links` added on detail only.
- [x] Capture new Trip status transitions and an immutable accepted-vehicle snapshot in migration `040_trip_staff_evidence.sql`. Detail reports `complete_since_create` only when a creation event exists; older Trips honestly report partial/unavailable history. The current vehicle is labeled `current_driver_profile`, and settlement is a Driver assertion, not provider-confirmed payment.
- [x] Launch route access decision: Admin receives quoted distance and duration only. Quoted route geometry and traveled location history remain closed; any future access needs a purpose-specific permission and retention decision.
- [ ] Verify historical fare, market, and policy evidence against a populated historical database when one is approved. **Why open:** the approved disposable local PostGIS database had no older Trips, so its passing tests prove new capture and API behavior only. No populated historical database was approved or queried. Old vehicle snapshots and status events cannot be reconstructed from current rows; older Trips must continue to show partial/unavailable history. **Close when:** an approved populated database is sampled and the available/missing evidence is recorded without rewriting old Trips.
- [x] Add exact Rider, Trip, or Support case ID search via `/admin/riders?q=<uuid>`, plus account status and permissioned debt, Support, and Safety links on Rider detail. Contact-based search is excluded from the launch contract.
- [x] **Rider restriction contract:** Safety or Super Admin can block new Trip creation only with a Driver-opened Safety case linked to that Rider's Trip, a Rider-specific reason, and a future review date. Existing Trips continue. Review does not silently expire the restriction; Safety or Super Admin must lift it with an audited reason. Migration `041_rider_moderation.sql` and `/admin/riders/:riderID/restrictions` provide the record, history, and mutation.
- [x] **Staff-created Rider decision:** No production staff role, including Super Admin, can create a Rider account or mark a phone verified. Rider self-registration retains phone OTP. Test simulations use isolated test fixtures or a test-only CLI, never a production Admin route. The preview `Add Rider` form must remain disabled outside preview until its frontend contract changes.
- [x] **Rider flags:** Support can create case-linked support/payment flags; Safety can create incident-linked Safety flags from Driver-opened cases for the Rider's Trip; Super Admin can do either. A reason and future review date are required. Role-scoped history and an audited resolve action are available through `/admin/riders/:riderID/flags`. Flags never restrict an account automatically.
- [x] Add typed, permissioned Overview attention items with entity ID, priority, owner, assignee, destination, and counts drawn from the same queue predicates. Add `/admin/live/markers` with Operations request pickup markers and Safety incident markers based on the assigned Driver's recent location. Markers have source and timestamp; request markers omit route geometry.

### Partner review, verification, and inspection

- [x] Provide Driver review list/detail, profile and vehicle correction, manual identity review, document links/review, inspection report upload/review, and final eligibility decision.
- [x] Replace the newest-100-only Driver list with cursor pagination and a status filter.
- [x] Provide paginated document submission and inspection histories, including review and outcome timestamps without exposing storage keys in broad responses.
- [x] Guard account-deletion approval against active Trips and active Driver offers in the transaction; enforce Compliance/Super Admin permission and expose the number of files awaiting secure cleanup.
- [x] Filter one-star Driver ratings on the server before pagination; preserve the pending-review guard and Safety ownership.
- [x] Add Partner search by exact Driver ID or display-name prefix and filters for review status, fresh connected online state, published operating market (`zone`), Operating Balance band, and earliest required-document expiry band. `/admin/drivers` returns these indicators without coordinates; Compliance sees balance bands only, while a staff account with both Partner and Finance read permission (or Super Admin) can see exact kobo. Partner detail links recent Trips, performance, and safe Support/Safety summaries without conversation or incident evidence.
- [x] Add a paginated `/admin/drivers/:driverID/verification-timeline` for Compliance/Super Admin with application creation, document submission/review, inspection report/review, and profile/vehicle/manual/final decisions. It returns safe metadata and redacted changes, labels older coverage `partial_legacy`, and omits storage keys and raw evidence. Reviewer assignment and SLA remain deferred because no launch rule was agreed.

Inspection remains an externally supplied report workflow. An inspection-centre, slot, booking, or partner-login product is not specified or implemented; it is not an open backend task without a product decision.

## P1 — launch operations beyond initial read contracts

### Safety, Claims, and Support

- [x] Support topic and case list/detail, messages, replies, assignment/status updates, and read receipts.
- [x] Create a Safety incident from a Safety Support case; provide incident list/detail, severity/status/assignee changes, and an action timeline. Safety owns decisions; Support owns the conversation.
- [x] Provide the no-show policy and claim decision flow. Approval creates Rider debt; it does not immediately credit a Driver.
- [x] Add authenticated active-Trip SOS intake with an idempotent client request ID, high-priority Safety incident, and an unacknowledged SOS alert queue. Reporter coordinates carry capture time and source; a fresh Driver coordinate is a labeled fallback. Only Safety/Super Admin can read the one-time coordinate. SOS responses require acknowledgment before other states; every response carries a reason and actor. Resolution starts a 30-day coordinate purge, run by the backend maintenance job. There is no emergency-service dispatch integration.
- [x] Add staff-originated Support cases for an existing Rider/Driver or linked Trip with a recorded staff actor and reason; require an escalation reason and retain the action. Existing attachments use authorized short-lived URLs. SLA due times are deferred by launch decision.
- [x] Add `lost_item` and `complaint` topics and typed outcomes (`resolved_no_money`, `referred_to_finance`, `item_returned`, `unable_to_resolve`, `withdrawn`) with a reason and actor. Only fare/payment cases can hand off to the Finance queue. A Support outcome never transfers money.

### Finance and governed money

- [x] Provide staff Operating Balance account/entry reads, fare/payment case metadata, and disputed settlement snapshots. Finance and Super Admin can read these routes.
- [x] Govern existing Operating Balance, statutory-charge, and no-show policy drafts, approvals, and retirements with distinct staff actors.
- [x] Define launch money scope: full refunds of successful Driver top-ups, versioned Admin-configured reason codes and kobo limits, Finance or Super Admin proposal/approval by different people, and backend/provider safeguards. See [launch money rules](../../../backend/docs/ADMIN_LAUNCH_MONEY_RULES.md).
- [x] Implement refund policy drafts and distinct-staff approval, source-verified full top-up proposals, Driver-supplied and encrypted bank destinations, Compliance-reviewed legal-name matching, a different-staff refund approval, Nigeria-local-day kobo limits, reserved Operating Balance, durable single-send claims, provider initiation/status reconciliation, signed terminal webhooks, ledger reversal, and append-only action history. Local PostGIS and mocked-provider tests pass. Execution defaults off through `HEYGO_MONNIFY_REFUNDS_READY=false`.
- [ ] Verify the merchant's claimed sandbox/live refund activation and actual destination-account, fee, signed webhook, status lookup, and reconciliation behavior with controlled provider transactions before turning `HEYGO_MONNIFY_REFUNDS_READY` on. **Why open:** the backend passed local PostGIS and mocked-provider tests, but no controlled transaction was sent through this merchant's sandbox or live Monnify account. The team's activation confirmation is recorded, not transaction-verified. **Close when:** controlled provider refunds exercise the destination, fee, terminal webhook, status lookup, duplicate/uncertain response, and ledger reconciliation in the intended environments. Retain an unresolved reservation for any uncertain provider result; do not represent a proposed or pending refund as paid.
- [x] Expand Finance reads with source links on Operating Balance entries and cursor-paginated top-ups, statutory charges, arrears, Driver-asserted settlement history, no-show debt payments, legacy checkout payment state, and Support-to-Finance handoffs. The response distinguishes Driver assertions from provider evidence. New SQL paths were checked on disposable local PostGIS; populated staging and provider reconciliation remain release checks.

### Markets, Pricing, and Policies

- [x] Provide geofence list/detail, draft, different-staff approval, and retirement request/approval.
- [x] Provide versioned Operating Balance, statutory-charge, and no-show policy workflows with backend approval guards.
- [x] Correct new quote pricing to convert OSRM duration seconds to minutes before applying the per-minute rate; retain the distance input in meters for its per-meter rate. Existing quoted fares are not rewritten.
- [x] Add owner-module pending approval queues and safe decision receipts for geofences, pricing, Operating Balance, statutory charges, no-show, and refund policy. They show proposer/approver IDs, times, effective period, safe change summary, target, and whether the caller may approve; existing two-person mutation guards remain authoritative. There is no universal Approvals inbox.
- [x] Add unsaved geofence map preview and saved-draft conflict preview. Both show same-kind spatial/effective-period conflicts with safe zone ID, code, version, and dates; approval rechecks conflicts under a transaction lock. Local PostGIS tests cover a conflicting preview.
- [x] Add Finance-owned, market-scoped pricing versions for four packages with bounded base/per-km/per-minute kobo rates, quote simulation, different-staff approval, effective dates, retirement request/approval, and immutable published rates. New Rider quotes require exactly one approved pickup market and effective rate, apply meters/seconds with explicit unit conversion, and store the pricing version and market on the expiring fare. Admin Trip detail links to the frozen version; older fares show no pricing version. No launch rates are seeded, so each launch market must be published before quoting. Local PostGIS tests cover quote calculation, fail-closed market selection, version capture, and approval. Surge, promotions, and other preview-only controls remain out of contract.
- [x] Define launch cancellation/document/safety behavior without a duplicate generic policy service. The existing Admin-configured no-show wait and fee govern Driver claims after arrival; a Rider cancellation permits a claim only if it occurred after that wait. Finance review is still required to create Rider debt; cancellation itself never charges. Required-document expiry blocks Driver online eligibility at actual expiry. Safety restrictions require a linked case, reason, review date, and audited lift, and do not auto-expire. Document and restriction blocking rules are fixed launch controls, not editable Admin thresholds. See [pricing and policy contract](../../../backend/docs/ADMIN_MARKET_PRICING_AND_POLICIES.md).

## P2 — product scope to decide later

- [x] Provide Resend settings read/save/test without returning the stored secret. Live delivery is a separate release check.
- [ ] Define Promotions eligibility, audience, budget, approval, redemption, and reporting before adding staff APIs.
- [ ] Define consent-safe Communications templates, audience, scheduling, delivery status, and audit before adding staff APIs.
- [ ] Define Analytics aggregates, filters, denominators, freshness, and drill-down IDs before adding staff read models.
- [ ] Scope additional Settings by owning domain. Broad marketplace, security, integration, and health toggles have no generic backend save route.

## Release and integration checks — open

- [ ] Apply Admin role and Safety migrations in the target environment; deliberately assign existing staff roles. Verify the two-person setup, combined permissions, denied ungranted access, and no self-approval in staging. See [backend operations](../../../backend/docs/BACKEND_OPERATIONS.md).
- [ ] Configure `ADMIN_FRONTEND_URL` and Admin Resend settings. Implement the Admin accept-invite, verify-email, and reset-password pages to read the fragment token, clear it, and submit it to the matching backend endpoint; verify actual email delivery. The backend routes exist, but the frontend pages are not part of this backend task.
- [ ] Map the Admin frontend to these exact APIs and test complete journeys with real staff sessions. Preview interactions use local fixtures/session state and do not prove backend mutations.
- [ ] Verify private document access, account deletion and file cleanup, Support/Safety linkage, Finance reads, and policy approvals against staging data.
- [ ] Verify live Resend delivery, market/payment setup, signed Monnify events, location freshness, operational alerts, and deployed services before calling Admin launch-ready.

## Contract rules for remaining work

- Use `{ data, meta.nextCursor }` for paginated lists, stable IDs, UTC timestamps, explicit statuses, and integer kobo for money. Specify filters, sorting, empty state, and freshness for each read.
- Return only role-appropriate detail. Broad lists should link to records rather than copy identity numbers, private files, contacts, raw routes, or unrestricted event histories. Keep private files behind short-lived authorized URLs.
- Mutations need backend permission and CSRF checks, source-state validation, reasons for decisions, conflict responses, and audit receipts. Money or external delivery also needs idempotency and distinct staff actors where approval is required.

Source references: [Admin route registration](../../../backend/services/api-gateway/handler/admin_auth.go), [permissions](../../../backend/services/api-gateway/handler/admin_permissions.go), [Trip/Rider reads](../../../backend/services/api-gateway/handler/admin_trip_rider.go), [Live Operations](../../../backend/services/api-gateway/handler/admin_live.go), [Driver review](../../../backend/services/api-gateway/handler/admin_review.go), [Safety](../../../backend/services/api-gateway/handler/admin_safety.go), [Finance reads](../../../backend/services/api-gateway/handler/admin_finance_reads.go), and [audit reads](../../../backend/services/api-gateway/handler/admin_audit_read.go). Source and local tests do not establish migrations, deployment, provider delivery, or frontend integration.
