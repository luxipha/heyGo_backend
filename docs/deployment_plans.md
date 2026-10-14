Create a clean baseline commit from the large current working-tree revamp.
Rename active previous-owner identifiers.
Run a full-history secret scan and dependency/license review.
Strengthen CI:- build
- unit tests
- PostGIS and Pub/Sub emulator integration tests
- race detector
- vet/staticcheck/govulncheck
- Docker builds

Launch authentication and Driver approval:
- use email OTP now through `POST /auth/email/otp/request` and `/auth/email/otp/verify`;
- retain `POST /auth/otp/request` and `/auth/otp/verify` for optional phone OTP later;
- configure `OTP_PEPPER`, `HEYGO_ADMIN_SETTINGS_KEY`, and Resend in HeyGo Admin;
- apply migration 063, configure Kudi API key/approved Corporate Sender ID/App Name Code/OTP Template Code in Admin Settings, then enable SMS OTP; generic `SMS_PROVIDER_*` environment settings no longer select the production sender;
- verify access-token, refresh-token rotation, logout, expiry, retry, resend, and abuse-limit behavior;
- keep CasperID identifiers nullable and its trust worker disabled unless CasperID credentials are configured;
- manually verify Driver identity in Admin, then approve only after the required documents and inspection pass;
- smoke-test Rider access, Driver pending access, blocked Go Online, approval, Go Online, and account/session revocation.

Add Google Cloud infrastructure and Workload Identity Federation.
Make the services Cloud Run-compatible.
Deploy immutable commit-SHA images to staging.
Run migrations, smoke tests, health checks, and WebSocket/gRPC checks.
Promote the same image digests to production with approval and rollback support.

## Rider Ride Agent release follow-up

The optional separate `ride-agent-gateway` service now has source, a non-root Dockerfile and image-build coverage. Terraform and the release workflow now include its IAM-protected service, dedicated identity, internal-token/Places-key secrets and gateway URL/audience. Populate the Places key and verify real gateway-to-agent access and provider retention/attribution before activation. LLM planning remains disabled. See [RIDER_RIDE_AGENT.md](RIDER_RIDE_AGENT.md) for version 1 contracts and [RIDER_REVIEW.md](RIDER_REVIEW.md) for verification and unchanged old UI follow-ups. Migrations 054–055 add atomic quotes, matching deadlines and legacy fare-consumption backfill. No deployment has been performed for this work.
