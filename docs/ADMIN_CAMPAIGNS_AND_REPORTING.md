# Admin reporting and campaign authoring

Implemented on 2026-10-19. No production deployment was performed.

## Reporting

- `GET /admin/support/summary` requires `support.read`. Counts cover all recorded cases, independently of queue pagination. Open means not resolved/closed. Awaiting staff response means open/in-progress with a latest user message (or no messages). Escalated means an unresolved case with a recorded escalation. First-response minutes average first staff reply minus case creation, excluding cases without a reply. Resolved/closed share uses all recorded cases as denominator. Categories, assigned-agent workloads, latest case updates, priority/escalation alerts, and recorded referrals to Finance are included. Agent presence, channel attribution, CSAT, SLA compliance and approved refund amounts remain unavailable.
- `GET /admin/analytics/summary?days=7|30|90&marketCode=...` requires `trips.read`. Cohort membership is trip creation time within the rolling interval. Daily buckets use UTC. Completed-trip fare totals sum stored fare snapshots for completed records in that cohort. They are not payment confirmation, platform revenue or paid-out earnings. Activity is distinct riders / assigned Partners with cohort trips. Repeat riders have multiple cohort trip records; this is not cohort retention. Status distribution is current status, not a reconstructed historical funnel. ETA, signup growth, commission/payout aggregates and channel attribution are not inferred.

## Campaign drafts

Apply `066_admin_campaign_drafts.sql` before using these routes:

- `GET /admin/promotions/campaigns` and `GET /admin/communications/campaigns`: UUID cursor pagination, limit 1–100 (default 25), all-draft totals, and execution capabilities.
- `GET .../campaigns/:campaignID`: load an actual record independently of list pagination.
- `POST .../campaigns`: create a draft with a client-generated UUID and change reason.
- `PATCH .../campaigns/:campaignID`: edit a draft using its current version and change reason. Stale versions and duplicate IDs return 409, without overwriting an existing draft.
- `POST .../campaigns/:campaignID/activate`: authenticated/authorised/CSRF-protected, always 409 while execution rules remain unapproved.

Read permissions are `promotions.read` and `communications.read`; mutations require the corresponding `.manage` permission and staff CSRF. These four permissions are initially assigned only to the existing `super_admin` role. No new staff roles or production role assignments were created. Existing sessions should refresh Admin to reload navigation permissions.

Draft fields: name (1–160 characters), category, intended audience description (1–240), proposed rules/message text (1–4000), proposed budget in kobo (0–1e12; communication drafts must use 0), version on edits, and change reason (5–1000). Unknown fields are rejected. Monetary budget is a proposal, not a fund reservation or spend. Audience descriptions are not executable segment definitions. Message channels are intent only, not evidence that a provider is configured.

Draft writes and a staff audit event commit in one transaction. The audit references the campaign ID, version, actor and reason, without copying message text into staff audit diffs. The database constrains every campaign status to `draft`, so no UI/API flag can enable execution accidentally.

## Decisions required before execution

Promotion rules must specify fixed/percentage benefits, caps, eligible trips/users/markets, stacking, redemption limits, validity windows, approval ownership, budget reservation/accounting, concurrency and reversal/refund treatment. Referral and Partner-incentive policies need their own award triggers.

Communications require approved channels/providers, resolvable audiences and consent/preferences, permitted transactional/marketing purposes, content/template policy, scheduling/timezones, approval ownership, idempotency, rate limits, provider receipts and failure/retry rules. Sending is not implemented or activated by saving a draft.

Execution services, redemption accounting, provider delivery workers, activation migrations and measured performance reports remain future work after those contracts are agreed. No discounts, payments, SMS, push notifications, WhatsApp messages or emails were issued during this implementation.

## Verification

- Gateway handler unit/regression tests passed.
- Real isolated PostgreSQL tests passed for aggregate SQL, empty/filtered cohorts, draft persistence, optimistic concurrency, kind isolation, audit writes and the database activation constraint.
- Admin typecheck, 47 tests across 11 files, and production build passed. The existing large JavaScript chunk warning remains.
- Isolated local gateway rebuilt/restarted; `/ready` returned 200. Authenticated reads of all four new route families returned 200. Activation returned 409; the test session was logged out.
- Browser rendering, production deployment, production migration and external provider behavior remain separate verification gates.
