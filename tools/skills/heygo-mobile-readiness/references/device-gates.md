# Physical-device and provider gates

Select gates relevant to the change and platform; do not claim unrun gates passed.

- Fresh install and returning login using production configuration. Disabled development/bypass flags do not prove real login/map initialization.
- OTP available channels, PIN/2FA/recovery and expired/revoked session handling. Test logout/login to another account and late asynchronous responses.
- Notification permission denied/granted, registration/token refresh, preference enforcement, foreground display and background/cold-start taps. Tap reaches the authorized destination without duplicate UI. Check iOS APNs separately.
- Navigation/maps keys and app restrictions, location denied/revoked/background behavior, chosen provider, trip stage transitions and weak/offline connectivity. GPS freshness must not be fabricated.
- Evidence capture/upload/failure/retry, private media authorization and pending/approved review behavior where applicable.
- One real authorized trip journey, history counts/pagination/refresh, earnings period changes, supported payment states and support acknowledgement. Use controlled accounts; never run financial operations without authorization.
- Small screen, text scaling, safe areas, contrast/reduced motion and shared sheets. Record screenshots/simulator evidence when reporting visual quality.
- AAB/APK signed certificate matches intended upload identity, package ID and version code. Inspect with installed Android tooling; static Gradle text does not verify it. Keep keystore/private credentials off repositories.
- Shorebird release app/version/platform alignment; distinguish Dart patch from native/store build. Record artifact hash and exact commit.
