# Driver lessons to apply to Rider

Use these as review questions, not assumed Rider bugs.

- Map every preference to a single owner: server, device-persistent or intentional transient UI. Duplicate controls bind to that same owner. Verify server rejection and rollback, hydration and cross-account isolation.
- Sign-out/account change: unregister the correct device, clear account-scoped caches, close sockets/listeners and ignore late responses. Verify A → logout → B without A's data/token registration returning.
- Notifications: registration, refresh-token rotation, removal, consent/preferences, foreground display, background and cold-start tap routing. FCM acceptance does not prove device receipt. Scheduled broadcasts need scheduled execution evidence as well as UI save.
- OTP/PIN/2FA: backend access stays restricted until required factors succeed. Recovery verifies the intended identity, preserves 2FA policy, invalidates appropriate sessions and does not expose secrets. UI-only locks are not authentication.
- Trip lifecycle: accept/start/complete/cancel follows authoritative state/events. Countdown uses actual expiry; Keep waiting really dismisses/continues. Calling opens the phone caller with the actual authorized rider/driver contact.
- History/earnings: backend totals versus loaded-page counts; refresh, pagination, period changes and errors. Do not invent balances, bonuses, routes, names or success receipts.
- Evidence upload: private reservation → upload → finalize/submit → staff preview/access control. Cancellation/retry failures retain entered details. Quick-add chips must enter the submitted payload.
- Navigation: persisted provider choice affects actual launch; in-app default and installed-app handling. Hidden toll/flood or scheduled features stay deferred when requested. Do not infer provider support from a switch.
- Reviewed identity/vehicle records: distinct pending/rejected/expired/approved and inspection states; read-only approved details, PIN gating where specified; actual submitted images, no decorative demo model claimed as driver data.
- Settings: actual session revoke, support/Mantis acknowledgement, in-app legal URLs and real OS permission state. No inert search/store-rating affordance.
- Source scans: tests-only references and unused exports are candidates. Follow generated/routed entrypoints before calling them dead. Never automatically delete.

Suggested verification sequence: boot/signed out → login → onboarding/review → ready home → trip → history/payment → settings/support → logout/account switch. Rider may have a different approved journey; preserve it.
