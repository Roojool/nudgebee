# NuBi queued follow-ups: validation and rollout

PR #37946 merged on September 12, 2026 (`f4efbd5fc6`). It introduced persisted
follow-ups and migration V922 (`1788790550733`). The owner confirmed rollout
configuration and migration were updated. This is distinct from an independent
deployment probe.

## Verified behavior

Local Chrome testing on September 10 verified queue → edit → resubmit → automatic
completion (`EDIT_OK`), deletion while waiting, cancellation, draft preservation
after completion/cancellation, and submission after stopping (`FINAL_OK`).
The final focused frontend run passed 74 tests; frontend lint/type/format checks,
focused backend queue tests, migration version checks and Atlas validation passed.
All 10 Gemini review threads were resolved. The labeler failed its single-commit
policy; technical PR checks passed.

These are local observations, not production latency measurements. Earlier polls
took roughly 2–5 seconds and one cancellation took 8.3 seconds. Three database
round trips through the local proxy took 244.5–246.5 ms each. Text-only admission
now skips a redundant preflight read; its deployed speedup has not been measured.

## Optimistic submission follow-up

The frontend clears the composer immediately and shows Saving… while the existing
queue request runs. Only the server acknowledges persistence and dispatches work.
On confirmation, the pending row is replaced by the saved queue entry.

An explicit admission rejection retains text and images with Move to composer.
A timeout or uncertain response retains the pending text and offers Refresh
history. Refresh only reads history: it never retries the submission. Without a
server message ID, matching by prompt text cannot establish whether it was saved,
so uncertain entries are not automatically discarded or made sendable. Inspect
history before deciding to submit again. Temporary rows are scoped to the current
chat and are not durable across page reloads.

Edit and Delete retain the existing confirmed flow. Edit retains its progress indicator.
This follow-up adds no backend changes, tables, migrations, endpoints, or automatic
retries. It uses the deployed V922 queue implementation unchanged.

## Latency follow-up

This change removes the acknowledgement wait from composer clearing; it does not
reduce backend latency. Deployed before/after measurements remain outstanding.
Use existing request traces and DAO duration logs to isolate transport, authorization,
locking and persistence before adding infrastructure or changing polling.

## Remaining live coverage

- Concurrent users and capacity, including retries when full.
- Lost acknowledgement after commit; history refresh must never resubmit.
- Send Now permissions, stale targets and late old-turn finalization.
- Attachments, configured model/agent routing and mixed authors.
- Approval/client-tool waits and dispatch transitions.
- Navigation during pending operations and same-text successive submissions.
- Backend saturation/restarts and before/after deployed latency measurement.

Conversation-level worker backpressure is deferred under #37948. Earlier broad
local runs had unrelated frontend fixture failures and backend dependency/timeouts;
focused passing tests do not establish that the complete suite passes.
