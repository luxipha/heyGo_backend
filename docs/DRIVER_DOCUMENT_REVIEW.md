# Driver documents and partner inspection review

This backend implements the seven confirmed online requirements: explicit CasperID NIN verification, five HeyGo reviewed documents, and a completed vehicle inspection. A HeyGo staff approval is required after all seven. CasperID driver identity and HeyGo staff username/password sessions are separate.

## Storage and staff setup

- Configure a private Cloudflare R2 bucket with `R2_ENDPOINT`, `R2_BUCKET`, `R2_ACCESS_KEY_ID`, and `R2_SECRET_ACCESS_KEY`. Document routes return 503 until it is configured. The R2 token must be scoped to that bucket. Set bucket CORS to permit the Driver app origin to PUT `image/jpeg`, `image/png`, and `application/pdf` with the signed headers.
- Set `ADMIN_COOKIE_SECURE=true` on HTTPS deployments. Set `ALLOWED_ORIGINS` to the actual staff UI origin, and `ADMIN_COOKIE_SAME_SITE=none` only if the staff UI is on a different site and HTTPS is in use.
- Apply migrations by starting the gateway or running `go run ./cmd/admin-user <username>` with `DATABASE_URL` set. The CLI prompts for a password and creates the first staff account. It does not update existing accounts or expose registration over HTTP.

## Driver document flow

1. `POST /driver/documents/uploads` with `type`, `side`, `contentType`, `sizeBytes`. Upload the bytes to the returned PUT URL with exactly the returned headers.
2. `POST /driver/documents` with `type`, `uploadIds`, optional `expiresAt`. Driver license needs `front` and `back`; the other four need `document`. The server checks R2 size and MIME type before recording `under_review`.
3. If the current document is rejected, call `POST /driver/documents/:id/resubmit` with the same submission body and fresh upload IDs. A new submission or resubmission clears prior staff approval and takes the driver offline unless on a trip.

## Staff review flow

- `POST /admin/auth/login` with `username` and `password` returns a 12 hour HttpOnly staff session cookie and `csrfToken`. `GET /admin/auth/me` restores the session; `POST /admin/auth/logout` revokes it. Send the CSRF token in `X-CSRF-Token` for every staff mutation.
- `GET /admin/drivers?status=pending|approved|rejected|all` and `GET /admin/drivers/:driverID` show the review queue and details. The queue returns up to 100 newest records; pagination is still needed for larger operations.
- `GET /admin/drivers/:driverID/documents/:documentID/files/:side` returns a short lived private GET URL. `POST /admin/drivers/:driverID/documents/:documentID/review` accepts `{ "status": "approved" }` or `{ "status": "rejected", "reason": "..." }`.
- The independent inspection partner emails HeyGo its report. A staff member reserves a report upload using `POST /admin/drivers/:driverID/inspection/uploads`, uploads the emailed file to the returned R2 URL, then records the partner, physical location and upload ID via `POST /admin/drivers/:driverID/inspection`. `GET /admin/drivers/:driverID/inspection/:inspectionID/report` provides a short lived private GET URL. `POST /admin/drivers/:driverID/inspection/:inspectionID/review` accepts `completed` or `failed` plus a reason for failure. There is no partner login, slot or booking API.
- `POST /admin/drivers/:driverID/approval` accepts `approved` or `rejected` plus a reason for rejection. Approval fails until all seven requirements pass. Review mutations are audited in `admin_audit` and driver updates are recorded in `user_events` for `GET /driver/events` recovery.

NIN stays incomplete until CasperID supplies an explicit NIN verification claim. Do not infer it from generic identity verification. The linked credit score is outside this workflow.
