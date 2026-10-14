# Rider notifications and delivery

Implemented 2026-10-12. Rider ownership is enforced independently of Driver routes. Migration 060 adds Rider preferences, session-bound devices, inbox items and durable push delivery records. No cloud deployment or real provider delivery is claimed.

## Public contracts

All routes require an active authenticated Rider. Responses use the existing `{data, meta}` envelope.

| Route | Contract |
| --- | --- |
| `GET /rider/notification-preferences` | `rideUpdates`, `accountAlerts`, `promotions`, `updatedAt`, `scope: push_only`, `inboxAlwaysRetained: true`. Defaults are true/true/false. |
| `PATCH /rider/notification-preferences` | Partial boolean mutation; at least one field. Persisted read returned. Disabling suppresses pending pushes; inbox state remains authoritative. |
| `POST /rider/devices` | `{id: UUID, platform: ios or android, pushToken, appVersion}`. Stable per-account installation ID; registration binds to the current authenticated HeyGo session. Same owned ID rotates the token. Foreign IDs and active token collisions return 409; no transfer from an active foreign session. A token from a revoked/expired session can be released. Token values are never returned. |
| `DELETE /rider/devices/:deviceID` | Idempotent, owned removal; disables/clears token and cancels queued delivery. A foreign ID has no effect. |
| `GET /rider/notifications?cursor=...` | Up to 30 items, total owned `unreadCount`, and `meta.nextCursor`. Numeric cursor is an exclusive earlier item ID; newest first. Invalid cursor returns 400. |
| `GET /rider/notifications/:id` | Owned item plus versioned target hint; foreign/missing returns 404. |
| `PATCH /rider/notifications/:id` | Idempotent owned read acknowledgment with `readAt`; foreign/missing returns 404. |
| `POST /rider/notifications/read-all` | Marks currently stored owned unread items; later arrivals remain unread. |
| `GET /rider/notifications/:id/deliveries` | Owned statuses/attempts/retry dates and generic reasons. No device token, provider response or private credentials. `sent` means provider accepted, not device receipt/read. |

## Typed targets and privacy

Inbox `data` is `{version: 1, target: {kind, id}}`. Approved kinds are `trip`, `receipt`, `support_case`, and `debt`; `id` is a UUID. Debt means the owned trip's no-show claim/approved debt read, never a generic wallet. Receipt and debt targets enter the existing owned trip page with its authoritative panels. Support enters the actual owned case conversation. No URL, arbitrary route, Admin operation or action instruction is accepted.

Push data is exactly `{notification_id, audience: rider, schema_version: 1}`. Its title/body are generic; coordinates, route, names, phone, chat/Support body, trust score, amounts, case detail and identity links stay out of the lock-screen payload. Incoming push data is only an inbox lookup identity. The app first fetches the owned notification, validates its typed target, and re-reads trip/receipt/no-show or Support state before navigation. The mounted journey also performs its own recovery reads. Taps never confirm, cancel, pay, rate or send SOS automatically.

Signed-out taps are retained as a secure local pending inbox identity until sign-in. Foreign/deleted entries fail ownership reads and are discarded; network failures remain recoverable. Foreground messages refresh the inbox. Cold/background tap handling uses Firebase `getInitialMessage` / `onMessageOpenedApp`; token changes and app resume re-register the stable owned device. These SDK entry points follow [Firebase's Flutter receive-message documentation](https://firebase.google.com/docs/cloud-messaging/flutter/receive-messages).

## Fan-out, delivery and recovery

Approved lifecycle/matching/settlement/no-show/chat/Support/SOS intake events generate allowlisted inbox items. Resource ownership, Rider role and account activity are checked, including dual-role recipients. Fan-out shares the account row lock so deactivation/preference changes cannot race an inbox insertion after reviewed cleanup. Location/read/command-result events do not create push noise. Each inbox item is unique by owner/source event; each device delivery is unique by notification/device. Durable event, inbox and device fan-out commit in the same transaction. `rider.notification.created` carries only the inbox identity on the Rider stream.

The existing FCM sender is reused. Polling gateway workers and authenticated maintenance passes alternate Driver/Rider work; Driver tables/routes remain separate. Rider sends additionally require `HEYGO_RIDER_PUSH_ENABLED=true`, configured FCM credentials, an enabled owned device, active/unrevoked/unexpired registration session and enabled category. The worker rechecks these conditions and the current token just before sending. Revocation/removal/preference changes suppress unsent work; a provider request already in flight cannot be recalled.

Jobs use `SKIP LOCKED`, leases, exponential retries and an eight-attempt bound. Expired leases recover; stale attempt completion cannot overwrite a newer attempt. An unregistered old token cannot disable a newly rotated token. Transient failures remain pending; exhausted/crash-ambiguous delivery is `dead` with a generic reason, including `retry_limit_unresolved`. Delivery is at least once: a crash after provider acceptance can cause a duplicate push. The stable inbox ID permits duplicate presentation suppression; provider acceptance never proves delivery to a person.

Gateway now subscribes to `trip.event.created`; Terraform messaging configuration adds that gateway subscription. Deployment must provision it. URL credentials are stripped before gateway request logging/tracing; legacy socket query authentication is normalized to a header. The native Rider socket supplies `HEYGO_WS_ORIGIN` (default local `http://localhost:3000`), which must appear in gateway `ALLOWED_ORIGINS`.

Rider privacy archives include notification preferences and owned inbox data, excluding push tokens. Reviewed account deletion removes Rider inbox/device/preferences alongside the existing account cleanup.

## App and activation gates

The three existing preference rows retain their layout and labels; toggles wait for persisted acknowledgment/reread and disable during unavailable/loading states. A new inbox uses Rider spacing, colors, radii and typography. Transparent Material wrappers preserve the old toggle layout while fixing Flutter rendering assertions; first-frame loading avoids notifying a previous route during build. Notification rendering/navigation has a dedicated widget test.

Firebase packages are pinned to the generation compatible with the app's existing iOS 14 minimum. This project uses its existing CocoaPods path after SwiftPM dependency fetching failed; see [Flutter's project-local configuration](https://docs.flutter.dev/packages-and-plugins/swift-package-manager/for-app-developers). Native permission/background/APNs entitlement wiring is present, with development entitlements for Debug and production for Release/Profile. Matching Apple provisioning and Firebase/APNs credentials remain required.

- [ ] Select/verify the controlled Rider Firebase project and sender credentials. No sender credential was configured or printed here.
- [ ] Register Android package/iOS bundle `com.heygo.rider`, install matching client configuration and upload the correct APNs credential. Current Android JSON does **not** contain that package; iOS `GoogleService-Info.plist` is absent. Existing unrelated client configuration was not repurposed.
- [ ] Resolve the existing Android Gradle 7.6.3 / Flutter-selected Java 21 incompatibility, then verify Android build/release signing and matching Firebase config. The attempted debug build failed with `Unsupported class file major version 65`; automatic migrator edits from that failed check were reverted while preserving the existing package/minimum SDK.
- [ ] Verify iOS signing/provisioning and both platforms on actual installed devices. Only an iOS simulator was available.
- [ ] Enable server flag and build with `--dart-define=HEYGO_RIDER_PUSH_ENABLED=true` only against the matching controlled setup; set `HEYGO_API_URL` and `HEYGO_WS_ORIGIN` consistently.
- [ ] Observe actual foreground/background/cold-start delivery, denial/re-enable, token rotation, logout/account switch, category suppression, stale targets and restart recovery. Record provider message ID, device/OS/build and observed receipt/tap; do not put tokens/credentials into evidence.
- [ ] Observe deployed authenticated maintenance/outbox execution and service/broker interruption recovery. Local emulator behavior does not prove cloud IAM, Scheduler or provider delivery.

See `RIDER_VERIFICATION_EVIDENCE.md` for separate source, automated, local staging and provider/live evidence.
