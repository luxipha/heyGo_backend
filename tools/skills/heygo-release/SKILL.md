---
name: heygo-release
description: Prepare, reconcile and verify HeyGo backend, Admin and Flutter releases using Git-linked artifacts, existing deployment tools and explicit production actions. Use for release readiness, deployment, rollback and notification infrastructure checks.
---
# HeyGo release

Start by identifying the exact repository and requested target. Discover the HeyGo root from the user's workspace; do not assume Rider is Driver. Read applicable AGENTS.md. Inspect Git status including staged and untracked work. An authorization to deploy does not authorize committing unrelated changes, modifying IAM, deleting files or importing catalogue data.

## Git and production consistency

Run `python3 scripts/release_check.py --repo /path/to/backend`. If reconciling a direct deployment, add `--snapshot /path/to/backend/.dev/releases/TAG/source-manifest.json`. Differences and extra runtime files must be reviewed; a source match does not prove the files are committed. Preserve user work and never broad-stage mixed changes.

Prefer reviewed commit → push → passing audit and image builds for that same SHA → explicit deployment → production checks. Never silently replace this with a local image deployment. The deploy workflow defaults to the four current services; Ride Agent is opt-in with `--include-ride-agent` and must already exist. Recheck infrastructure rather than inventing/deploying a missing service. `scripts/ci_release.py --repo PATH --commit SHA` inspects only. `--execute --confirm SHA --backup-id ID` dispatches the existing workflow only after clean main, matching remote SHA, successful push audit/image runs, a verified successful database backup within 24 hours and required Cloud Run services. Dispatch is not completion: watch the workflow and verify exact deployed revisions. This wrapper does not repair the workflow or create backups; verify required database backup and migration/rollback plan before dispatch. Do not execute local fallback scripts just because CI is blocked.

## Choose the target

For production targets and rollback procedure, read [references/targets.md](references/targets.md), then the actual repository's `docs/PRODUCTION_RELEASE_RUNBOOK.md`. Treat recorded revisions/hosts as historical until rechecked.

- Backend: inspect workflow, migrations, backup and image digest/SHA, runtime identity, secrets references and rollback traffic. Preserve environment/secrets. Test a no-traffic candidate before promotion. Do not infer that `/ready` proves auth, payments, uploads or push.
- Admin: use the existing production build command. `heygo-mobile-readiness/scripts/run_checks.py --app ADMIN --kind admin --execute` runs it (resolve the installed sibling skill directory). Reuse `backend/scripts/publish-admin-production.py` for authorized hosting publication only after reviewing its current source, build and backup behavior. It uses FTPS and replaces the entry point last. It is separate from backend GitHub CI: do not claim an Admin Git-driven deployment exists without evidence.
- Mobile: use `$heygo-mobile-readiness` before the requested AAB/Shorebird release. Confirm native changes need a store build; consult current official Shorebird guidance before deciding patch eligibility. Do not auto-upload to stores.

## Notifications and HTTP verification

`python3 scripts/notification_check.py` reads current FCM template/IAM/Scheduler settings. `--validate-only` executes the retained non-delivering validator after checking its known image, command and identity; this is an explicit cloud operation. Never create a user broadcast as a test without the user's instruction. Check the serving revision, queued/scheduled job behavior and physical-device delivery separately.

`python3 scripts/verify_web.py --url https://n.heygo.ng --dist ADMIN/dist` compares HTTP index/assets. It does not render the app. A Cloudflare automated-client 403 needs browser/origin verification, not disabled protections. Use the available browser workflow for rendered checks.

Report commit, artifact digest/tag, migration/backup, deployed revision, tests and remaining provider/device checks. Keep credentials out of output and source; filenames/key references are sufficient. Stop on failed prerequisites; preserve rollback/evidence, and do not retry mutations blindly.
