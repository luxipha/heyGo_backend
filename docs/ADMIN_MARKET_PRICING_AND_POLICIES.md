# Admin market pricing and launch policies

## Published market fares

Finance or Super Admin creates a pricing draft for an approved market through `POST /admin/pricing`. The draft contains one rate for each package (`bike`, `auto`, `sedan`, `suv`), with `baseKobo`, `perKmKobo`, and `perMinuteKobo`, plus an effective period. Base is 0–1,000,000 kobo; distance and time are each 0–10,000 kobo per kilometer/minute. A quote must be positive and at most 10,000,000 kobo. `POST /admin/pricing/:id/simulate` accepts `distanceMeters` and `durationSeconds` and returns the rounded kobo fare for each package; it never saves a quote.

A different Finance or Super Admin account approves the draft. Approval rejects overlapping effective periods for the same market, and the database makes published rates immutable. Retirement is a request and different-person approval that sets a future `effectiveUntil`. The existing fare quote API requires exactly one approved pickup market and one effective pricing version. Without either, new quotes fail closed. It calculates `base + distanceMeters × perKm / 1000 + durationSeconds × perMinute / 60`, rounds to kobo, and stores the pricing version and market code on each 15-minute quote. A Trip retains its original quoted fare and applied version. There is no seeded/default market price; Finance must publish every launch market before Rider quotes can succeed. Existing saved quotes are not rewritten.

`GET /admin/pricing?marketCode=&status=all|draft|approved` lists versions; `GET /admin/pricing/:id` returns a version and its four rates. The approval queue and receipt paths described below apply. Surge, zone multipliers, promotions, minimum fares, waiting charges, and service fees have no server-enforced formula in this version. Admin preview values for those fields are examples only.

## Module approval queues and geofence preview

Each owning module has `GET .../pending-approvals`, `GET .../:id/decision-receipt`, and, where retirement exists, `GET .../:id/retirement-requests/:requestID/decision-receipt`. The modules are geofences, pricing, Operating Balance policies, statutory-charge rules, no-show policies, and refund policy. Queues return safe identifiers, proposer, effective dates, a redacted rule summary, target path, and whether the current account may approve. Receipts show proposer, approver, decision time, state, and the same safe summary. Owner permissions and the different-person guard still apply to each mutation; there is no cross-module approval inbox.

`POST /admin/geofences/preview` accepts an unsaved `kind`, GeoJSON `boundary`, `effectiveFrom`, and optional `effectiveUntil`; `GET /admin/geofences/:id/conflicts` previews a saved draft. Both return approved same-kind spatial/period conflicts with zone IDs, codes, versions, and effective periods. They are advisory. The geofence approval transaction rechecks conflict under its lock.

## Cancellation, documents, and restrictions

The Admin-configured no-show policy is the only launch Rider cancellation fee policy. A Driver who has recorded arrival may submit a no-show claim after the market's approved wait period. If the Rider cancelled, the cancellation itself must have occurred *after* that wait period. An earlier Rider cancellation has no no-show fee. A claim records the policy ID and fee, but only Finance approval creates Rider debt; cancelling or submitting a claim does not charge the Rider. No separate cancellation fee algorithm or duplicate policy family is introduced.

Required Driver documents block online eligibility when their actual expiry is reached. Compliance owns document review; this blocking rule is fixed for launch and cannot be weakened by an Admin setting. Safety or Super Admin may apply a case-linked Rider restriction with a reason and review date, and must record an audited lift. The restriction does not auto-expire at the review date. These are fixed safety controls, not editable threshold families. Any later editable document or restriction policy needs its own approved rule and enforcement design before Admin controls are shown.

Local PostGIS and Go tests verify the new migration and API behavior. Published markets and production-like historical data still need release verification in the intended environment.
