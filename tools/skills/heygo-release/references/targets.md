# HeyGo deployment context

Recorded 24 October 2026; verify before every release.

- Workspace: sibling `backend`, `apps/admin`, `apps/driver`, `apps/rider` repositories.
- Cloud project `heygo-ng`, region `europe-west1`, registry `europe-west1-docker.pkg.dev/heygo-ng/heygo`.
- Existing services: api-gateway, driver-service, trip-service, payment-service. Ride Agent absence blocked the five-service workflow at the last inspection; recheck, do not create it as a side effect.
- Migration job `heygo-migration`, Cloud SQL instance `heygo-postgres`. Confirm recent successful backup and migration scope before rollout.
- Firebase target `heygo-c5ae1`. API identity `api-gateway@heygo-ng.iam.gserviceaccount.com`; keyless ADC opt-in uses `FCM_AUTH_MODE=adc`, `FCM_PROJECT_ID=heygo-c5ae1`.
- Firebase target custom role `heygoFcmSender` grants only `cloudmessaging.messages.create`. Do not replace it with a broad admin role.
- `gateway-maintenance` runs every minute and processes broadcasts/notification jobs. The retained `heygo-fcm-validation` job calls `validate_only=true`; no device delivery is proven.
- Admin https://n.heygo.ng uses FTPS at the hosting origin. Current details and private credential location are in the backend runbook and publishing script. Do not copy credentials into a skill or report.
- Runtime source manifests under `.dev/releases/TAG/` allow local reconciliation; they are not substitutes for Git commits.

Existing scripts: `backend/scripts/promote-cloud-run-release.py` (direct deployment; not the preferred CI route), `backend/scripts/publish-admin-production.py` (Admin hosting). Inspect source before use. This skill does not imply permission for a new production release, IAM change or secret rotation.
