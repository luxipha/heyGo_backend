# Production release runbook

## Targets and private configuration

- Google Cloud project: `heygo-ng`; region: `europe-west1`.
- Public API: `https://api-gateway-392496443029.europe-west1.run.app`.
- Admin: `https://n.heygo.ng`.
- Admin hosting server: `da38.host-ww.net`, explicit FTPS on port 21.
- Admin document root: `/domains/n.heygo.ng/public_html`.
- Hosting account credentials are read privately from `backend/keys.md`; do not print them, copy them into scripts, or include that file in build contexts.
- Python.org macOS installations may lack a CA bundle. The publisher loads `/etc/ssl/cert.pem`; certificate verification stays enabled. The branded FTP hostname currently resolves elsewhere: use the documented DirectAdmin server.

## Validate and publish

1. Review the exact runtime source changes and migration inventory. Do not stage unrelated files or deploy speculative fleet classifications.
2. Run backend tests against isolated PostgreSQL. Run Admin typecheck, tests and `npm run build:production`.
3. Capture an immutable source manifest and publish immutable service/migration images. Keep local environments, `keys.md`, signing keys and provider credentials out of source snapshots. The 24 October release used daemonless OCI append to replace static Linux executables in the already deployed non-root runtimes; existing Dockerfiles/GitHub image publishing remain the normal container-build path.
4. Create and confirm a successful Cloud SQL backup before migration.
5. Update and execute the existing `heygo-migration` job with the release image; verify `succeededCount=1` before promotion.
6. From `backend`, run:

   ```bash
   python3 scripts/promote-cloud-run-release.py --tag RELEASE_TAG --deploy
   ```

   This records rollback revisions in `.dev/releases/RELEASE_TAG/`, creates no-traffic candidates for Driver/Trip/Payment/API, checks public readiness and auth boundaries, and promotes dependencies before the API. It preserves existing service environment, secrets, ingress and IAM. It does not deploy the absent Ride Agent service or change infrastructure permissions. Run without `--deploy` for inspection. If a run fails partway, inspect the recorded candidates/current traffic and resume deliberately; do not overwrite rollback records.
7. Publish Admin after API verification:

   ```bash
   python3 scripts/publish-admin-production.py
   python3 scripts/publish-admin-production.py --publish
   ```

   The first command only verifies the encrypted connection. Publishing preserves previous/overwritten files locally under `.dev/releases/TIMESTAMP/previous/`, retains all old remote assets and a timestamped old index, uploads assets first, then atomically renames the new index last. No remote files are deleted.
8. Verify live API readiness, auth channels, recovery route validation, Admin index/asset hashes and SPA routing. These checks do not send OTPs or prove FCM, payment, Mantis, capture or full signed-in journeys.

## Rollback

Use the exact prior 100%-traffic revisions from `.dev/releases/RELEASE_TAG/rollback.json`. For each service:

```bash
gcloud run services update-traffic SERVICE \
  --project=heygo-ng --region=europe-west1 \
  --to-revisions=PREVIOUS_REVISION=100
```

Restore the API first if necessary. Database migrations are not automatically reversed; retain the database backup and review compatibility before restoring data. For Admin, restore backed-up overwritten assets/config files and the previous index last over verified FTPS. Do not delete retained hashed assets during a release.

## Driver Play/Shorebird build

Driver upload signing is separate from backend/Admin. For first-time setup, `apps/driver/scripts/setup-upload-signing.sh` prompts locally and stores the upload key under `~/.heygo-signing/`, with ignored `android/key.properties`. Never put server credentials into Dart defines. `config/production.json` contains public origins and disabled development flags, not secrets. Android targets API 36 and release builds require upload signing.

Shorebird releases/patches must use the same production configuration. Keep the upload key/password and debug symbols backed up; native/plugin changes require a store release. A successful AAB build does not prove live provider delivery or complete store approval.

## Cloudflare verification note

Cloudflare Browser Integrity Check (`bic`) can return 403 to Python/curl even when the site works. During the 24 October release, the zone's A record already matched the hosting server, direct-origin HTTPS hashes and SPA routing passed, and public Chrome returned 200 with a rendered staff sign-in screen and no JavaScript errors. Do not change DNS or disable browser/security checks based only on a crawler 403. Browser checks use a fresh session and do not establish staff sign-in or provider delivery.

## FCM sender

Production uses keyless ADC with `FCM_AUTH_MODE=adc` and `FCM_PROJECT_ID=heygo-c5ae1`. Preserve these variables and the API service identity on future releases. Sender IAM, validation job and rollback details: [FCM production configuration](FCM_PRODUCTION_CONFIGURATION.md). No Firebase JSON key is required.

## Git reconciliation and CI release

The recorded 24 October runtime source is commit `59e1a6ee482e87306e3f5e0a6b611cbce6caf4b5`.
Hash inventory and provenance are in [release evidence](releases/2026-10-24/reconciliation.json).
This is source equivalence to the direct build, not a claim that the current
production image was built by GitHub Actions.

The production workflow deploys four existing services by default. Enable
`include_ride_agent` only when that service is already provisioned and in scope.
Audit and image-publish push runs must pass for the exact main SHA selected.
A reviewed backup ID is required; the operator must verify a successful recent
backup before dispatch (the current CI identity has no SQL backup-reader role).
The `ci-release` helper verifies it within 24 hours. CI records the backup reference
and uploads pre-migration image/traffic rollback evidence, without secrets.
A push/PR builds/tests images; deployment remains a separate manual action.

```sh
python3 scripts/heygo-tools.py release --repo .
python3 scripts/heygo-tools.py ci-release --repo . --commit FULL_MAIN_SHA
# Only when the release itself is authorized:
python3 scripts/heygo-tools.py ci-release --repo . --commit FULL_MAIN_SHA --execute --confirm FULL_MAIN_SHA --backup-id REVIEWED_BACKUP_ID
```

Unrelated local docs/Terraform/data changes were excluded from this reconciliation.
