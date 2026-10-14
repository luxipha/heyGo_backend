# Disposable Rider staging and evidence

Prepared 2026-10-12. A separate local staging database and limited staff account were created; the Compose service stack is **not deployed or running**. Docker is not installed on the current workstation. GCP has an active login for `heygo-ng`; the Cloud Run service list returned no services. No cloud resources, real SOS requests or production service interruptions were created.

## Local isolated stack

`scripts/prepare-rider-staging.py` creates `.dev/rider-staging.env` with random local-only credentials, mode 0600. It refuses to overwrite an existing file. The file is ignored by Git; do not paste it into this document. It never reads `.env.local`.

`scripts/rider-staging.compose.yaml` defines a separate PostGIS database, Pub/Sub emulator, gateway, Trip service, Driver service and Ride Agent. Published ports bind to loopback. Its synthetic project ID is `heygo-rider-staging-local`; this is an emulator namespace, **not a provisioned GCP project**.

From backend, after Docker is installed:

```sh
python3 scripts/prepare-rider-staging.py # already executed here; skip if the file exists
docker compose --env-file .dev/rider-staging.env -f scripts/rider-staging.compose.yaml config --quiet
docker compose --env-file .dev/rider-staging.env -f scripts/rider-staging.compose.yaml up --build -d
```

Intended local gateway: `http://127.0.0.1:8087`; emulator: `127.0.0.1:8086`; database: `127.0.0.1:55440`. These are configuration values, not currently verified running endpoints. Use the host's reachable LAN address for a physical device only after explicitly configuring a private test network; loopback on a phone points to the phone.

No real OTP/provider credentials are included. The synthetic test phone remains `+2348012345678`; database tests use a recording sender and do not deliver an SMS. The actual local staff record is `rider-staging-support` (ID `a1100000-0000-4000-8000-000000000004`), with only Support and Safety roles. A random password is stored as `RIDER_STAGING_STAFF_PASSWORD` in the private environment file and hashed using the backend's existing password implementation. The record uses the regular Admin username/password authentication path; no authentication bypass was added. No operational staff session or responder handling has been observed.

## Created local database and staff account

The existing isolated PostgreSQL cluster remains running on loopback port **55439**. A new, separate database `heygo_rider_staging` was created there, migrated through 061 and populated with the guarded synthetic Admin journey fixtures. It is distinct from `heygo_rider_auth_test`. The staff seed was run and its allowed roles verified; no password/hash was printed. This database is available locally, but no gateway or broker is attached to it.

Reproduce the seed from backend:

```sh
RIDER_STAGING_DATABASE_URL='postgres://abisoye@127.0.0.1:55439/heygo_rider_staging?sslmode=disable' go run ./scripts/rider-staging-seed
```

The seed accepts only `heygo_rider_staging` on localhost ports 55439/55440, checks existing identities before migrations or fixture writes, refuses unrelated users/staff, and grants only Support/Safety. Use the Docker database's private connection credentials when seeding the future Compose target on port 55440; it is a different database instance.

The local cluster lives at `/private/tmp/heygo-rider-auth-pg`. To stop it after use:

```sh
/usr/local/opt/postgresql@17/bin/pg_ctl -D /private/tmp/heygo-rider-auth-pg stop -m fast
```

Python preparation and YAML syntax were validated; Docker Compose schema/runtime validation remains unexecuted because Docker is absent.

## Verification procedure and acceptance

1. Start the isolated stack, check readiness, and migrate before seeding. Use `scripts/seed-admin-staging.sql` only with `PGOPTIONS='-c heygo.staging_seed=enabled'` against this fresh database; its guard rejects databases containing non-staging users. Never use the existing `.env.local` database for this seed.
2. Configure a controlled OTP sender/test inbox and sign in with the seeded staging staff identity. Exercise Rider issue/SOS intake, open the actual linked case in Admin, assign/reply/resolve it, and verify Rider read-through/replies with real IDs. Capture case/incident references and staff action timestamps. Intake `received` does not mean emergency responders were dispatched.
3. Against this stack only, pause the broker or consumer, enqueue a command, verify it remains queued/unresolved, restart delivery, and verify durable command result, one lifecycle transition, outbox retry and duplicate suppression. Emulator success does not prove deployed authenticated push, Cloud Scheduler sweeper or Cloud Tasks behavior.
4. For cloud staging, provision a separate database, staging-named services, dedicated topics/subscriptions, service accounts and push audiences, offer-expiry queue and authenticated sweeper job. Use immutable images and apply migrations through 061. Record URLs, revision IDs, IAM checks, actual sweeper executions and recovery evidence before marking the deployed gate complete. The local pull-mode configuration does not provision these cloud components.
5. On installed Android and iOS devices, test dialog dismissal, secure-storage restart, background/resume, reconnect/replay and lost command/reply responses. Compare the preserved widgets at the approved viewport. Record device/OS/build and observed outcomes.

## Evidence obtained here

- Isolated local PostGIS tests: Rider Profile/feedback/history ownership, trust snapshot recording/deduplication/clearing, chat restrictions, SOS intake and replay, generic Support user/staff workflow, Finance handoff and safety intake reads.
- Local outbox test: stable event identity survives acknowledged publication followed by failed commit; this uses a controlled test publisher, not deployed Pub/Sub.
- Flutter regression tests and native iOS simulator build are separate from authenticated device/provider verification. No physical Android/iOS devices were detected.
- Docker Compose runtime validation, broker restart experiment, cloud deployment and real operational staff handling remain open.

## OSM and Google decision remains deferred

Both providers remain the requested product direction. No new provider selection, Google activation or OSM adapter is assumed here. Before activation, agree search versus routing responsibilities, stable provider-prefixed place IDs, fallback behavior, storage/retention, map attribution and provider-specific display requirements. Never silently substitute one provider's coordinates for another provider's place ID. Legacy trips without a resolvable destination remain unavailable for automatic rebooking; Rider must choose a new destination and receive a fresh preview.

## J-24 local handoff observed

Migration 060 is applied. `scripts/rider-staging-check` starts a temporary loopback gateway with actual session/role/staff/CSRF middleware against the guarded seeded database. It completed synthetic phone OTP interception, real session/refresh/logout, current/history/Profile/preferences/device reads/writes, limited staff case reply, persisted Rider inbox/read and authenticated WebSocket replay. The seeded Rider is bound to synthetic `+2348012345678`; OTPs are intercepted in memory and no SMS is sent. The gateway closes when the command finishes; no full Trip/Driver service stack was attached. Staff replies/inbox evidence remain in this disposable database. See `RIDER_VERIFICATION_EVIDENCE.md` for reproduction and boundaries.

The official Pub/Sub emulator was installed and started for verification, then stopped after the checks. It ran separately on loopback `127.0.0.1:8086`, using synthetic namespace `heygo-rider-staging-local`. Actual publish with consumer offline, consumer recovery and duplicate replay passed. No GCP project was provisioned and no deployed broker was interrupted. Cloud IAM/maintenance/sweeper/provider/device gates remain open.


### J-20/J-21 follow-up (2026-10-12)

The guarded staging checker now applies migration 061 after validating the existing disposable fixtures. The controlled HTTP check passed Rider profile storage and `/auth/me` recovery, account-bound email proof/linking, and owned debt/payment history reads in addition to its existing auth/Support/inbox checks. The synthetic Rider display name is `Staging Rider`; its email is `rider.identity.staging@example.test`, verified using an intercepted code. No real SMS/email or checkout/provider request was sent. The temporary gateway closed and its registered synthetic device/session were cleaned up. Evidence: `/private/tmp/heygo-rider-j20-staging-final.log`. Database-backed contact-change concurrency/session-revocation and fake-provider uncertain-checkout recovery are separate automated evidence, not staging phone/provider proof.
