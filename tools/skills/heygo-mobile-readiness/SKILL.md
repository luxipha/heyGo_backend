---
name: heygo-mobile-readiness
description: Check HeyGo Driver or Rider Flutter release configuration, artifact credential leaks and local analyze/test results, then identify remaining authentication, notification, trip and physical-device release gates.
---
# HeyGo mobile readiness

Identify Driver versus Rider explicitly; the current IDE directory is not authorization to work on the other app. Read its AGENTS.md, mounted router, app configuration and Git changes. Preserve existing product flows and design tokens. Treat old readiness docs and disabled dev flags as evidence to verify, not release guarantees.

## Automated checks

`python3 scripts/mobile_check.py --app APP --output /tmp/heygo-mobile.json` inspects Flutter version metadata, Android identity/signing hints, Firebase native files, Shorebird presence, known credential signatures and development flag defaults. It prints no matching source contents. Optional `--defines PATH` checks a private JSON define file without echoing values. Optional `--artifact PATH.aab` scans ZIP entries for private-key/service-account/database credentials and development URL candidates, returning entry names only. Public Firebase client keys/project IDs are not backend secrets. A scan cannot prove complete absence of secrets, correct signing, active runtime configuration or provider acceptance.

`python3 scripts/run_checks.py --app APP --flutter /path/to/flutter` prints the analyze/test plan. Add `--execute --output /tmp/heygo-checks.json` only when running local validation is within scope. It runs analyze then tests, stops on failure and records status/output hash without dumping potentially sensitive logs. If it fails, rerun the failing command with appropriate private log handling to diagnose it. It never creates/uploads an AAB or Shorebird release. A configured tool path is required when Flutter is not on PATH.

## Complete release evidence

Read [references/device-gates.md](references/device-gates.md) for the requested platform/journey. Confirm signed release identity, version/build number, actual production API/defines, native provider restrictions and necessary permissions. Reuse existing release configuration; never embed backend secrets, service-account JSON, signing passwords or unrestricted native credentials. Verify current store/platform requirements from official sources before claiming compliance; no SDK target floor is baked into this skill.

For Play/Store/Shorebird work, inspect actual signing/build scripts and current official guidance, then provide or execute the requested command. Verify the resulting artifact rather than merely trusting a configured signing block. Never claim a patch supports native changes without checking. No release/upload, provisioning or signing-key rotation is implicit in a readiness audit.

Keep four levels separate: source/static checks, local test/build, deployed/provider behavior, physical-device/store checks. AAB scans and passing widget tests do not establish real push, maps, OTP or payment delivery. Report remaining checks explicitly. Use `$heygo-integration-audit` if controls/contracts need tracing and `$heygo-release` for authorized deployment.
