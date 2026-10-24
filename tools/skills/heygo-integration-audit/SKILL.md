---
name: heygo-integration-audit
description: Audit HeyGo Driver, Rider or Admin controls against state, persistence and backend contracts; find inert controls, placeholders and potential dead code, and produce evidence-based gaps without automatic edits or deletion.
---
# HeyGo integration audit

Establish the exact checkout and mounted entrypoint before auditing. Read applicable AGENTS.md and inspect Git status. Rider has legacy and newer flows; identify the active route from main/session routing instead of auditing an unmounted screen. Driver composition is normally `lib/ui/driver_app.dart`, with state in `lib/state/driver_controller.dart`; confirm current files. Reuse established components and semantic design tokens when changes are subsequently authorized.

Run `python3 scripts/integration_audit.py --app APP --backend BACKEND --output /tmp/heygo-integration.json`. This script emits path/line candidates without source excerpts. Empty/disabled callbacks, local preference writes, fixed progress, direct dialogs, unavailable copy and no-other-reference symbols need human review. Comments and strings can match; a disabled button may be intentional. It does not prove dead code or a missing endpoint. API call names and literal backend routes are navigation aids, not an automatic contract match.

For each requested feature trace: mounted control → callback/state owner → client request → registered route/auth → handler/service/storage → event/notification → rendered result. Inspect migrations, permissions, pagination, uploads and tests as relevant. Distinguish absent, existing-but-unwired, partial and verified. Record source file/line evidence and the exact verification level. Preserve private values; no source snippets from keys/env or request payloads in generated reports.

Use [references/driver-lessons.md](references/driver-lessons.md) for cross-app review. Transfer invariants, not Driver UI/state wholesale. For a visual audit use screenshots/browser/simulator evidence; this static script does not replace rendered verification.

Report a compact table: feature, mounted path, state owner, contract, observed gap, evidence level, next action. Separate static/local, deployed API, provider acceptance and physical-device evidence. Do not call a preference connected simply because its label updates. Do not mark absent data as zero, successful or verified.

`python3 scripts/propose_todo.py --report /tmp/heygo-integration.json --todo BACKEND/docs/todo.md --output /tmp/heygo-todo-proposal.md` produces a separate proposal, never modifies the todo or checks off static candidates. Merge only confirmed findings when requested. Nothing in this skill authorizes deletion; ask before removing retained code and do not change deferred-feature visibility without scope.
