# Bug-report integration

Rider and Admin call HeyGo endpoints. `shared/bugreports.Provider` is the replaceable boundary; `shared/bugreports/mantis` is the MantisBT implementation. No Mantis credentials, project IDs, issue IDs or response objects are sent to either client.

Mantis stores report titles, descriptions and updates. Migration `065_bug_report_links.sql` stores only HeyGo ownership, provider references, submission identity/hash and creation time. It does not mirror report content. Existing customer support cases remain separate.

## Configuration and activation

The API gateway requires these runtime variables:

```dotenv
BUG_REPORT_PROVIDER=mantis
MANTIS_URL=https://issues.heygo.ng
MANTIS_RIDER_PROJECT_ID=2
MANTIS_RIDER_CATEGORY_ID=2
MANTIS_RIDER_CUSTOM_FIELDS_JSON={"1":"Rider","2":"Rider App","4":"Unknown"}
MANTIS_API_TOKEN=<runtime-secret>
```

Read-only inspection on 2026-10-18 confirmed MantisBT 2.28.4, project 2 (`heyGo Rider App`) and category 2 (`Bug`). Supply the token through the existing runtime secret mechanism. The code does not read `keys.md`; do not copy that file into images or clients. Prefer a dedicated integration account with the project permissions necessary to report, view, add public updates and change status.

Before activation: apply migration 065 using the normal migration runner, supply configuration/secret, and release the backend and clients. These are release steps, not actions performed by this implementation. Confirm a real Rider report is created in the expected project/category; check its status and a public update from Admin, private-note exclusion, ownership denial for a second rider, and provider outage behavior. Mock-server tests establish the API contract but do not prove the installation's write permissions or custom workflow settings.

## Screens and API

- Rider: Support → Bug reports → Report a bug. Title and description only in this version. Users submit using their HeyGo session and can list their own references and read report status/public updates.
- Admin: Support → App bug reports. Lists reports submitted through HeyGo, opens details, changes status and sends rider-visible updates. It does not import pre-existing/unlinked Mantis issues. `support.read` permits reads; `support.manage` and the existing CSRF check protect writes. Staff write intentions are audited before remote calls; an intention entry does not prove delivery.
- Rider API: `GET/POST /bug-reports`, `GET /bug-reports/:reportID`. Creation requires a UUID `requestId`, title (3–150 characters), description (10–10000 characters). List uses one-based `page`, 20 entries per page and `hasMore`.
- Admin API: `GET /admin/support/bug-reports`, `GET/PATCH /admin/support/bug-reports/:reportID`, `POST /admin/support/bug-reports/:reportID/comments`.

Mantis issues are created private. Public notes (visibility ID 10) are the explicit customer-facing channel and may be displayed to the report's owner. Private and unknown-visibility notes, reporter identity, attachments and internal fields are excluded. This version has no screenshot upload, rider follow-up comments, background sync, or notifications. Status and updates are fetched on opening/refreshing a report. Status values are normalized to `open`, `in_progress`, `waiting_on_user`, `resolved`, `closed`.

## Interrupted delivery

The backend reserves a submission reference before the external request. Mantis receives the HeyGo reference in `additional_information`. Replaying the same `requestId` and payload returns the saved reference without another Mantis create. A changed payload conflicts. Rider securely retains the submitted payload and identity on the device, scoped to the signed-in account, until success is confirmed.

An interrupted external create may have succeeded. Such a reference remains `unconfirmed`: we do not report success or automatically submit a duplicate. The user gets the reference and a direction to contact support. An operator must check Mantis for the exact `HeyGo reference: <UUID>` marker, verify the configured instance/project, and reconcile `bug_report_links.external_id` with the verified issue ID. If no issue was created, the operator can release the reservation only after establishing that the original request cannot still complete. Preserve ownership and submission identity. No report content is backed up in HeyGo, so Mantis backups remain necessary for historical report recovery.

Comments have no automatic retry. If delivery is uncertain, Admin requires a refresh/check before allowing another send. Status updates are absolute assignments and can be checked by refreshing after failure.

## Unplugging or replacing Mantis

Set `BUG_REPORT_PROVIDER=disabled` (or unset it) and restart the gateway. Routes remain stable and return an honest unavailable response. Credentials can then be removed through the normal secret-management process. Existing Mantis reports and HeyGo reference rows are preserved. Re-enabling the same instance/project restores access.

Implement `Provider` and select it in `newBugReportAPI` to add another tracker. No Rider or Admin API/UI changes are required. Provider keys include the tracker instance/project so references cannot accidentally resolve against another configured instance. Replacing a provider does not migrate historical reports; migration of content and reference rows is a separate operation.

## Required fields on the production tracker

The Rider project requires Reporter Type (field 1), Source (field 2), and Platform (field 4). Configure the values above on the server; field 1 must allow `Rider` as well as the existing `Driver`. `Unknown` is the tracker's existing platform option and is truthful until client platform metadata is explicitly wired. The API does not infer Android/iOS from user input. App Version remains optional. All custom-field mapping stays inside the Mantis adapter; Rider and Admin contracts remain provider-neutral.

The production hosting firewall rejects Go's generic HTTP user-agent with 403. The adapter explicitly identifies itself as `HeyGo-BugReports/1.0`, retaining the same token authentication and verified HTTPS. Upstream rejection logs include method/status only, never token values, report text or internal issue IDs.
