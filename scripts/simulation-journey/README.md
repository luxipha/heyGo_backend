# Isolated Lagos journey simulator

Run from `backend`, with the local stack already started by
`./scripts/dev-isolated.sh` and the existing approved synthetic fixtures applied.
No production DSN, project, API URL, socket URL or host override is accepted.

```sh
python3 scripts/simulate-journey.py                  # guard validation only
python3 scripts/simulate-journey.py --run             # complete journey, 25 Drivers
python3 scripts/simulate-journey.py --run --scenario=cancel
python3 scripts/simulate-journey.py --run --scenario=decline
python3 scripts/simulate-journey.py --run --scenario=reconnect
python3 scripts/simulate-journey.py --run --scenario=stale-gps
python3 scripts/simulate-journey.py --run --scenario=no-driver
python3 scripts/simulate-journey.py --verify-latest    # latest completed test receipt/history
```

`--drivers=1` through `--drivers=25` limits supply. Decline/rematching needs more
than one Driver. Keep runs sequential: they intentionally use the same fixture
Driver identities. Both HTTP and WebSocket handshakes honor bounded `Retry-After`
responses; localhost load is still subject to production-equivalent rate limits.

## What the runner does

- Adds a uniquely labelled synthetic Rider per run, keeping prior unpaid
  completions intact. All new identities use `.invalid` emails and the
  `heygo-simulation:v1:` namespace.
- Creates short-lived, hashed **fixture sessions** for only these synthetic
  accounts. Requests pass through real session/role middleware. This session
  bootstrap is not an email/SMS OTP or identity-provider test.
- Connects real authenticated Driver sockets, publishes fresh locations and goes
  online through unchanged eligibility/balance/market/socket checks.
- Resolves a real Places destination using explicit guided mode; obtains an
  actual OSRM road route and approved synthetic fare; explicitly confirms a
  booking, then repeats the exact idempotent request. It resolves the canonical
  fare-to-trip record before accepting any offer and ignores historical replay.
- Accepts a real socket offer, calls actual arrival/start/completion APIs and
  waits for authoritative Rider snapshots. Position samples follow real road
  geometry, accelerated for testing; heading is derived from the next sample.
  This is not a speed, duration, traffic or ETA benchmark.
- Connects an authenticated Rider socket and requires actual Driver position
  delivery. Presentation interpolation in Flutter cannot substitute for this.
- Confirms only one trip exists for the fare. Receipt/history verification uses
  authenticated APIs and checks NGN, matching reference and providerVerified=false.
  Actual direct settlement remains pending; it never invents a provider payment.
- On normal/error exit, closes owned sockets, requests those Drivers offline and
  revokes only sessions it created. It does **not** delete accounts, trips,
  inspections, events, audit records or prior sessions. Failed/unfinished trips
  are retained for investigation. No automatic reset or record cleanup exists.

The no-Driver case expects automatic backend cancellation with
`matchingOutcome=no_drivers` and no current-trip offer; persistent pending is not
the intended successful result. Cancellation uses a confirmed Rider command;
decline expects a different Driver; reconnect keeps the accepted assignment;
stale GPS must leave the persisted fresh location unchanged.

## Evidence and limits

Initial full journey passed with 25 online Drivers, 273 road points and exactly
one booking. Cancellation, decline/rematching, reconnect and stale GPS passed.
Reconnect delivered 33 movement events to the Rider socket. The final no-Driver case also passed with zero offers and matchingOutcome=no_drivers.
All five scenario results are recorded in `/tmp/heygo-simulation-scenarios.json`. History/receipt verification passed with actual settlement `pending`.

The current fixtures are a synthetic Lagos metro rectangle, not an official
state boundary, with synthetic rates and inspection evidence. No live customer,
real Driver, funded ledger, payment provider or production dispatch is contacted.
Native map rendering, widget lifecycle regressions, authenticated socket delivery
and physically observed complete UI journeys are separate verification levels.
