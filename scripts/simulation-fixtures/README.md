# Local Lagos simulation fixtures

This command creates 25 synthetic, offline Driver identities in the named local
Rider staging database. It does not create a booking, fake socket presence, move
cars, send mail, issue auth credentials or change production.

From the backend directory, load only the ignored isolated profile:

```sh
set -a
. ./.dev/admin-local.env
set +a
go run ./scripts/simulation-fixtures --routing-demo
```

The default is a transaction rollback/dry run. After inspecting its result:

```sh
go run ./scripts/simulation-fixtures --apply --routing-demo
```

`--routing-demo` explicitly configures the previously approved public OSRM demo
for local testing. It refuses to overwrite differing existing Admin settings.
Without this option, geo settings are untouched. A public demo is not a production
routing service or SLA.

## Safety and limitations

- Only `127.0.0.1:55439/heygo_rider_staging`, the `heygo-admin-local` project and
  loopback Pub/Sub emulator are accepted. Database connection overrides are rejected.
- Stable IDs and identity-collision checks make reruns additive. There is no reset
  or deletion command. Existing unrelated accounts are preserved.
- `SIM_LAGOS` is a synthetic Lagos metro rectangle, longitude 3.20–3.60 and latitude
  6.40–6.70. It is not an authoritative Lagos State boundary. Interstate is deferred.
- Synthetic Sedan pricing: base 100,000 kobo, 7,500 kobo/km and 2,500 kobo/minute.
  Synthetic minimum operating balance is zero. These are not operational rates.
- Two disabled staff identities provide distinct synthetic maker/checker references.
  Document storage references and inspection records are explicitly labelled
  synthetic and are not real uploads, identity proof or physical inspections.
- Unchanged runtime eligibility functions are verified for all 25 Drivers before
  commit. They use the existing legacy Sedan category; this does not classify the
  real fleet or modify catalogue approvals.
- Driver sockets, fresh GPS and Go Online still have to happen through authenticated
  APIs before any Driver counts as available. Offline fixtures do not appear as
  real matching supply. The fleet/movement runner is a separate task.
- No arbitrary funded balance or payment-provider success is inserted.
- Reruns preserve existing fixture state. Expired or subsequently changed eligibility
  fails validation rather than silently reapproving or resetting evidence.
- Review/approve any future cleanup that deletes records separately.

## Destination gateway

The ignored mode-0600 `.dev/agent-local.env` is provisioned locally with a private
Places key and internal token. It is deliberately not included here. The API profile
must point to `http://127.0.0.1:8085` and have the same internal token.

```sh
python3 scripts/dev-agent.py
./scripts/dev-isolated.sh
```

The isolated launcher starts the optional destination gateway when its private
profile exists. The gateway binds loopback; Cloud Run retains the default PORT
binding when HTTP_ADDR is absent. The local helper permits AI only when explicitly enabled with the configured
Gemini adapter and private key; otherwise destination search stays deterministic. It neither fetches
secrets automatically nor changes IAM. The standard stop helper still targets the
original four services; the optional Agent has its own tracked PID in
`.dev/ride-agent-gateway.pid` and should be managed separately without stopping
unrelated listeners.

## Recorded checks (25 October 2026)

- Guard unit test rejects cloud hosts/projects, wrong ports, missing emulator and
  connection overrides.
- Dry run → apply → second apply all passed with exactly 25 eligible Drivers.
- Unauthenticated local Agent request returned 401.
- Existing Rider review login → real Google Places search/details → real OSRM-backed
  fare preview returned HTTP 200; the verification session alone was logged out.
- Preview: 273 road points, 14,691.6 m, 856.1 s. No booking was submitted.
- Alibaba model-list authentication failed at both supplied workspace and documented
  Singapore shared endpoints with 401/invalid_api_key. Correct the active key and
  workspace/region pairing before enabling Qwen; never print or commit the key.

- Native Google tiles over Lagos were verified after simulator rebuild/relaunch with
  `apps/rider/scripts/run-local.sh`. The native location-permission prompt is visible;
  permission behavior and a real authenticated online Driver offer remain unverified.

### Alibaba diagnostic correction

The earlier 401 result above was caused by a diagnostic extractor truncating the
key at a dot. The full key authenticates (workspace model list HTTP 200) and the
ignored private profile has been corrected. Inference with both Flash models
returns 403 `AccessDenied.Unpurchased`; check Model Studio activation/entitlement
in Singapore before enabling AI. No replacement key is required on the evidence
so far. Read complete credential field values, never infer provider key syntax
from a restrictive regular expression.

### Gemini verification

The backend Gemini adapter is connected locally using the first verified key and
`gemini-2.5-flash-lite`. Configure `RIDE_AGENT_PLANNER_PROVIDER=gemini`,
`GEMINI_MODEL`, `GEMINI_API_KEY` and explicit `RIDE_AGENT_LLM_ENABLED` in the private
Agent profile only. Restart only the tracked Agent after configuration changes.
Model credentials never belong in this README or mobile config. Provider 403/429,
redirect and invalid-tool responses retain deterministic place search. Actual
Rider API → Gemini → Places resolution passed with confirmation still required.
This adapter resolves destinations; it does not itself book or complete trips.
