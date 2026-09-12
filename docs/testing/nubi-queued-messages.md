# PR #37946: queued-message local testing

The fixes are in `feat/nubi-queued-messages`, merged locally with `origin/main` at
`55e62af9cd`. The merge is not committed or pushed. Runtime testing and human
approval remain outstanding.

## Start the local test

Apply the normal migration image to the local database; the queue enum migration
is now **V922** (timestamp `1788790550733`). V912 and V921 are already used by main.
The checksum was regenerated with `new-migration.sh --regen-sum`.

Start the local llm-server with its existing development dependencies and:

```sh
LLM_SERVER_QUEUED_MESSAGES_ENABLED=true
LLM_SERVER_MAX_QUEUED_MESSAGES_PER_CONVERSATION=5
```

The queue switch defaults to false. The model-capability response advertises the
switch and capacity to the UI. Reload the app after changing server configuration.
Use the existing service-local development instructions for connecting the app,
llm-server, PostgreSQL, and supporting services. Do not target production.

## Acceptance cases

| Case | Expected result |
| --- | --- |
| Switch disabled | Running investigation retains the old submission lock; no new queued messages are accepted. |
| Running turn + two followups | Both persist as QUEUED, visible in another browser, then execute one at a time in creation order. Closing the originating browser does not stop dispatch. |
| Two identical prompts | They remain separate messages with separate IDs. |
| Capacity 2, concurrent submitters | At most two queued rows; rejected requests retain their draft and images. Repeat with configured capacity 7 and verify the UI shows 7. |
| Completion races enqueue | Message either persists and dispatches once, or enqueue returns a retryable conflict with the draft retained. No permanently unclaimed new row. |
| New normal turn races automatic claim | New generation cannot overtake an already queued message or execute alongside it. |
| Edit success | Deletion finishes before text and attachments move into the composer. Resubmission contains the same images. |
| Edit/delete races claim | Conflict is visible; no editable duplicate of a message already executing. |
| Enqueue/Send Now network failure | Error is visible. No client-side fallback starts a second investigation. Reconcile uncertain network outcomes by refreshing history before retrying. |
| Switch account/chat during Edit or enqueue | The late response cannot overwrite the destination draft. Edited attachments do not follow the user to another chat. |
| Send Now valid target | Old active messages/agents/tool calls terminate; exactly the selected queued row is claimed. Other queued rows remain queued. |
| Send Now invalid/stale target | No active message is terminated. Repeat with an ID from another conversation/account. |
| Author authorization | Supplying another `user_id` cannot edit/delete/force their row. An account admin can manage rows only in their authorized account. Test both RPC and REST edit/delete forms. |
| Mixed-author queue | Each queued turn runs under its author's freshly loaded permissions, not the previous author or the person clicking Send Now. Revoke the author's access before dispatch and verify visible failure. |
| Approval/client-tool wait | Conversation becomes WAITING/WAITING_FOR_CLIENT_TOOL, queue does not advance until the active work finishes, and approval/resume behavior remains intact. |
| Force while old turn completes or fails | Late old-turn save-back cannot overwrite the forced turn or start another queued row. |
| Agent unavailable, malformed stored config, worker submission failure | Claimed message records a visible failure instead of remaining IN_PROGRESS indefinitely. Remaining queued work is retained for user action. |
| Screenshot + routing/model choice | Queued image reaches vision processing; requested agent routing and model/client-tool settings survive dispatch. Future queued prompts/configs are excluded from the current turn's history. |
| Poll between completion and next claim | UI continues polling while server queue remains; it observes the next turn without refreshing. |

## Automated checks in this review

- Focused queue API/DAO tests pass with `-race`, including new SQL regression checks. Final Go lint reports zero issues.
- Focused frontend tests: 72 passed across five suites.
- Frontend `npm run lint2`: passed (oxlint, TypeScript, temporal-dead-zone check, Prettier).
- Migration validation: one new migration clean, existing legacy warnings remain.
- Mechanical e2e selector sweep: no required selector removed. Repeated against
  the final working tree directly versus `origin/main` (the standard merge-base
  sweep overcounts main's changes before the merge commit): 10 changed frontend
  source files, 1,217 static selectors checked, 339 dynamic selectors not
  statically checkable. Affected areas: ClusterDetails and NuBi. Reviewed NuBi's
  `#set-config-btn` contract and the workflow composer; queue submission is
  restricted to investigation mode.
- Full frontend run: 40 suites failed / 358 passed, 239 tests failed / 4,356
  passed. Queue-focused suites pass. Examples outside this change: layout tests
  missing the `hasFeatureAccess` mock; an unclassified
  `integrations_diagnose_connection`; stale `dashboards:Execute` RBAC expectations.
  The queue's initial extra `ai_misc:Write` surface was corrected by classifying
  edit/delete as conversation actions; the focused RBAC rerun now reports only
  the unrelated dashboard expectations.
- Full `make validate` in llm-server: lint passed; full tests encountered missing
  PostgreSQL configuration, RabbitMQ connection failures, and ten-minute
  integration-test timeouts. Focused queue checks do not require those services.
- An isolated PostgreSQL run could not start: libpq client tools are installed,
  but the `postgres` server executable is absent. No live database behavior is
  claimed from SQL mocks.

- Next.js startup was attempted on port 3099. Default Turbopack rejects this
  worktree's existing node_modules symlink as outside its filesystem root.
  `next dev --webpack -p 3099` starts the server, but compiling the API route is
  blocked by missing/invalid `NUDGEBEE_ENCRYPTION_KEY` in the worktree's dev
  environment. Configure the normal local environment before browser/API tests.
  Both temporary startup processes were stopped; no production configuration
  was changed.

Keep runtime evidence with this checklist before committing/pushing and requesting
human approval. The repository requires affected-service validation before commit;
the broad-suite failures above are still outstanding.

### Local runtime findings (September 10)

Local Chrome testing on port 3000 exposed and corrected additional runtime issues:
- SQL finalization explicitly casts the status parameter to the PostgreSQL enum.
- Model capability loading retries after backend startup failures.
- Confirmed cancellation clears the running banner immediately and schedules a final refresh; failed refreshes remain retryable.
- Queued dispatch retains the authenticated author, uses a dedicated internal dispatch path, and does not reset cancelled messages through recovery cleanup.
- Queue submissions carry `queue_only`; failed admission returns an error rather than becoming a foreground asynchronous request. The composer accepts only `QUEUED` with a saved message ID, preserving drafts on other acknowledgements.

The last item was found when the first async acknowledgement arrived before its active message was persisted. Focused hook tests (37) and backend queue-admission tests pass. Final live checks below validate the basic queue and cancellation path; the full case matrix above still requires human testing.


Final local Chrome results:
- Follow-up `129735bc-d9ac-4172-9f9d-53ceb94d0419` received a `QUEUED` acknowledgement with its saved ID, appeared with its authenticated author, dispatched automatically, and completed with `QUEUE_OK`.
- A missed delta update left an earlier completed message cached as active. The fetcher now reconciles terminal-shell/active-message conflicts using one full fetch, replaces maps only on success, and guards stale responses after resets/overlap. This repairs terminal UI reconciliation; it does not redesign the backend delta cursor.
- Cancellation test message `53046d45-48ad-4abe-96d8-e63e5c471bc5` and its conversation both reached `TERMINATED`. The running banner disappeared approximately 8.3 seconds after the browser click. This is total observed latency, not isolated backend processing time. Poll requests observed earlier took roughly 2–5 seconds.
- Final hook/fetcher regression checks: 41 tests pass. Frontend lint/type/format checks pass. Focused backend queue tests pass. Review found no blockers in queue-only admission or reconciliation.
- No commits, pushes, deployment, or human approval performed. Broader baseline failures noted above remain distinct from these focused checks.

### Waiting-only UI and latency follow-up

- Removed the single-running-task banner; Stop remains in the composer. The queue header now reads `N waiting`.
- Measured three read-only `SELECT 1` calls through the local Cloud SQL proxy at 244.5–246.5 ms each. A browser app-config request took 5.6 seconds, while session lookup took 112 ms; these observations show latency beyond queue rendering, without isolating every backend stage.
- Text-only queue admission skips the redundant queue-list preflight. The locked transactional capacity/status checks remain authoritative. Image requests retain preflight before attachment persistence.
- Focused queue admission tests and 27 frontend queue tests pass; frontend checks and independent review pass. The backend optimization requires restart and a before/after live measurement; no end-to-end speedup is claimed yet.

### Browser self-test after edit fixes (September 10)

Chrome on port 3000, isolated session `048e5b64-f896-41f1-92c8-28efe834112c`; the original user session and draft were preserved. Browser actions were cross-checked against full conversation API reads.

- Queued B while A ran, edited B into the composer, changed its text, and resubmitted. Exactly one waiting entry appeared. Persisted message `0dcf1e46-198f-46f1-be43-4c1d3c4656f3` dispatched automatically and completed with `EDIT_OK`. Original B was absent.
- Deleted E while D was running: E disappeared and was absent from the full response.
- Edited F while D was running: F was removed from the queue/API and its text appeared in the composer. The draft survived D completing naturally.
- A deletion racing dispatch correctly returned 409 instead of deleting an active message. That message `6f7f2a26-8094-4130-b733-b9231607c09b` was subsequently stopped and confirmed `TERMINATED`.
- Found and fixed missing Stop when a draft occupies the composer. Both layouts now retain a separate Stop alongside Send. Live message `e27d81f8-df14-419d-a22c-2de097bc461c` reached `TERMINATED` while the draft remained unchanged. Three focused composer tests pass; independent review found no issues.
- A queue submission at the dispatch handoff initially returned 409 and retained its draft; manual retry succeeded. Added one retry only after confirmed non-admission and a fresh active-conversation read. This exact automatic-retry race has not yet been reproduced live.
- Pending queue cards have an opaque background. Waiting submissions in this test appeared once without a duplicate chat bubble. Server acknowledgement latency remains visible; these checks do not establish an end-to-end speedup.
- Resubmitted the edited draft after termination as a foreground request: `76ce1fad-703b-42c7-8b79-10e2e05bd67e` completed with `FINAL_OK`; the composer cleared on acceptance without the earlier queue-not-ready error.
- Final `lint2` (oxlint, TypeScript, TDZ, Prettier) passed. The legacy `lint` script fails because this Next version rejects `next lint --quiet`; the maintained `lint2` command was used instead.
