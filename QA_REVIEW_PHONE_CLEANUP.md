# Temporary Rider phone review — remove after 2026-11-19 QA

Scope: local QA only, never production. The removed temporary helper allowed the configured, active review-email account to link synthetic phone `+2340000000000` using its existing review-email code. It does not send an SMS, prove delivery, or provide a reachable driver contact.

Historical guards for the removed helper: explicit `HEYGO_QA_REVIEW_PHONE_ENABLED=true`; ENV and ENVIRONMENT both development; local project and Pub/Sub emulator; pool must target `127.0.0.1:55439/heygo_rider_staging`; exact synthetic phone; active verified Rider owning the active review email. Every confirmation rechecks the active review/code. Challenges keep normal expiry, attempt limits, owner binding, send budgets, and phone uniqueness checks. Code reuse stops at **2026-11-20 00:00 Asia/Ho_Chi_Minh (2026-11-19 17:00 UTC)**. Existing review-email configuration is unchanged.

## Cleanup status — 2026-11-20

- [x] Removed `HEYGO_QA_REVIEW_PHONE_ENABLED` and its temporary comment from `.dev/admin-local.env`.
- [x] Removed `services/api-gateway/handler/rider_phone_review_qa.go` and `rider_phone_review_qa_test.go` after removing their call sites/constants from `rider_phone_link.go`.
- [x] Removed the local QA request branch, code/expiry override, sender skip, and synthetic-phone confirmation guard from `rider_phone_link.go`. Normal SMS readiness and sender behavior are restored for every request; the file matches its tracked normal implementation. Unrelated work is preserved.
- [x] User reloaded normal gateway; verified runtime on 23 November. No production deployment.
- [x] Removed the synthetic contact only from the local review account after recording its prior phone values; expire its pending synthetic-phone challenges. Do not delete the account or its trips, or modify any real account/production records.
- [x] Ran the normal phone-link database integration test in an explicitly authorized disposable database and verify ordinary numbers still require a configured SMS provider. Focused OTP/SMS unit tests passed; see verification below.
- [x] Kept permanent contact-sheet layout fixes and their regression test.

Source cleanup is complete. The running API Gateway has not been restarted, and no database account/contact/challenge changes were made during this cleanup. The local review account therefore retains its previously verified synthetic contact until separately authorized cleanup; this is not a reachable real phone or evidence of SMS delivery.

Verification passed: six focused normal phone normalization, OTP binding, HTTP SMS sender and Kudi acceptance/rejection unit tests; all API Gateway packages compiled using `go test ./services/api-gateway/... -run '^$'`. `TestRiderPhoneLinkAndBookingReadiness` was explicitly skipped because `TEST_DATABASE_URL` was unset; no database integration run or live QA database changes occurred in this step. Keep this note until runtime restart and scoped contact/challenge cleanup are verified.

## Final verification — 2026-11-23

The historical pending statements above describe 20 November only. Final cleanup is now complete: normal phone-link disposable-database integration passed, temporary helper absent from runtime symbol scan, SMS channel reports disabled. After normal UI cancellation of the final owned trip, guarded local cleanup backed up and removed only the exact synthetic contact and its verification; pending challenges 0. Account and all 10 trips preserved. Active simulation runs 0, owned Driver active sessions 0, Rider active trips 0; cancelled trip no-show debt 0. Servers remain user-controlled. Evidence: `.dev/qa/evidence/20261123-video-cleanup/contact-cleanup-result.json`, `runtime-cleanup-result.json`, `heygo-phone-cleanup-tests.log`. No production changes or SMS delivery claim.
