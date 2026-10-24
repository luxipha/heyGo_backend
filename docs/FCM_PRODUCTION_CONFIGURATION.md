# Production FCM configuration

Configured 24 October 2026. API revision `api-gateway-00017-rir` uses image
`release-20261024-fcm-925fdcca3e37` in the existing `heygo-ng` registry.

- Runtime: Cloud Run API, project `heygo-ng`, region `europe-west1`.
- Firebase target: `heygo-c5ae1`.
- Environment: `FCM_AUTH_MODE=adc`, `FCM_PROJECT_ID=heygo-c5ae1`.
- Identity: `api-gateway@heygo-ng.iam.gserviceaccount.com`.
- Target-project custom role: `projects/heygo-c5ae1/roles/heygoFcmSender`, with only `cloudmessaging.messages.create`.
- FCM API enabled in the sender and target projects.

ADC obtains short-lived credentials from the attached runtime identity. No Firebase
JSON private key was created, embedded in images/apps, or added to Secret Manager.
The existing backend-only service-account-file mode remains compatible. ADC requires
explicit opt-in; a Firebase project ID alone does not enable sending.

`gateway-maintenance` is enabled every minute and invokes the authenticated gateway
maintenance endpoint, which processes scheduled broadcasts and notification pushes.
No manual broadcast was created by this deployment.

Validation: FCM unit tests and isolated PostgreSQL gateway/broadcast integration
suites passed. Candidate readiness, auth channels and Driver/Admin authentication
boundaries passed before promotion; production readiness passed after promotion.
Release source archive, hashes and rollback information are under
`.dev/releases/release-20261024-fcm-925fdcca3e37/`.

Firebase provider validation also passed: execution `heygo-fcm-validation-rdg4w`
returned `FCM validate_only HTTP 200` using the production API service identity.
No notification was delivered by the dry run. Maintenance remained successful
after promotion.

Physical-device foreground/background/cold-start delivery and iOS APNs still require
real-device verification. FCM acceptance is distinct from device receipt.

## Recheck without sending a notification

The retained Cloud Run job `heygo-fcm-validation` uses the same API service identity
and calls Firebase with `validate_only=true` against a dedicated validation topic.
It has no retries and does not deliver a notification. Run it with:

```sh
gcloud run jobs execute heygo-fcm-validation --project=heygo-ng --region=europe-west1 --wait
```

## Deployment and rollback

Future API releases must preserve both FCM environment variables, the attached
service identity and its target-project IAM binding. Use `--update-env-vars`, never
replace the complete environment. Prior revision `api-gateway-00015-tul` did not
configure FCM. To roll back this API release:

```sh
gcloud run services update-traffic api-gateway --project=heygo-ng --region=europe-west1 --to-revisions=api-gateway-00015-tul=100
```

Returning traffic to that revision also disables its notification sender. Preserve
queued jobs and inspect delivery state before retrying a failed broadcast.
