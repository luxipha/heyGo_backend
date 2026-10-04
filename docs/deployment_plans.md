Create a clean baseline commit from the large current working-tree revamp.
Rename active previous-owner identifiers.
Run a full-history secret scan and dependency/license review.
Strengthen CI:- build
- unit tests
- PostGIS and Kafka integration tests
- race detector
- vet/staticcheck/govulncheck
- Docker builds

Launch authentication and Driver approval:
- use email OTP now through `POST /auth/email/otp/request` and `/auth/email/otp/verify`;
- retain `POST /auth/otp/request` and `/auth/otp/verify` for optional phone OTP later;
- configure `OTP_PEPPER`, `HEYGO_ADMIN_SETTINGS_KEY`, and Resend in HeyGo Admin;
- configure `SMS_PROVIDER_URL`, `SMS_PROVIDER_TOKEN`, and `SMS_SENDER_ID` only when enabling phone OTP;
- verify access-token, refresh-token rotation, logout, expiry, retry, resend, and abuse-limit behavior;
- keep CasperID identifiers nullable and its trust worker disabled unless CasperID credentials are configured;
- manually verify Driver identity in Admin, then approve only after the required documents and inspection pass;
- smoke-test Rider access, Driver pending access, blocked Go Online, approval, Go Online, and account/session revocation.

Add Google Cloud infrastructure and Workload Identity Federation.
Make the services Cloud Run-compatible.
Deploy immutable commit-SHA images to staging.
Run migrations, smoke tests, health checks, and WebSocket/gRPC checks.
Promote the same image digests to production with approval and rollback support.
