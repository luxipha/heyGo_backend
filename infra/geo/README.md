# Geo cloud configuration

This isolated Terraform state owns geo API enablement, four app-restricted testing keys, a Places-only server key and a Maps billing alert. It does not touch the existing service/database foundation or deploy OSRM. Applied on 19 October 2026 with the user-approved $25 monthly Maps alert. Future changes still need a reviewed plan.

Inputs: `project_id=heygo-ng`, exact Android testing signing SHA-1, linked billing account ID, approved positive monthly USD alert, verified Cloud Billing catalog service IDs for Maps. API service names are not billing catalog IDs. Obtain catalog IDs from Cloud Billing before planning. Android identities are `ng.heygo.partner` (Driver) and `ng.heygo.rider` (Rider). iOS uses the same identities: `ng.heygo.partner` and `ng.heygo.rider`.

Run `terraform init`, `terraform validate`, then `terraform plan -out=geo.tfplan` with reviewed inputs. Do not print key strings or commit tfvars/state/plans. Inspect `terraform show` before approved apply.

After applying, fetch each native key directly into its app's ignored `ios/Flutter/Secrets.xcconfig` or local Gradle property. Never distribute the server Places key in mobile apps. Store it as a version of the existing `heygo-ride-agent-places-key` secret, using the existing runtime IAM boundary. The Places key has API restriction only; Cloud Run has no fixed egress IP to restrict without extra networking. Restrict secret access and cap Places quotas. Android testing keys match the current certificate only; Play signing needs its own certificate restriction before release.

Budgets notify at 50/90/100 percent and are not a hard spending limit. Configure per-API quotas after reviewing available metrics and a tolerable testing limit; do not invent an unsupported quota name or advertise an absolute dollar cap. Verify restricted keys in actual Android/iOS builds and confirm a wrong bundle/certificate is rejected.

Keep OSRM on the demo for testing through Admin Settings → Maps & navigation: choose OSRM, enter `https://router.project-osrm.org/route/v1/driving`, enable temporary demo mode and save with a reason. Later change to a supported public HTTPS `/route/v1/driving` endpoint, disable demo mode and save. New backend quotes/routes consume the new version without a mobile release. There is no automatic paid fallback.
