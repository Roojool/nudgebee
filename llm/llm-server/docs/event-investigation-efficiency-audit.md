# Event investigation and fix quality audit

Scope: repository review on 2026-09-18, covering triage, event investigation,
evidence assembly, the code handoff, and fix verification. This is not a new
production quality measurement. Historical production examples are recorded in
[issue #37005](https://github.com/nudgebee/nudgebee-enterprise/issues/37005).

## Findings and implemented fixes

| Gap | Evidence in the workflow | Change |
| --- | --- | --- |
| Interpretations can displace observations | `api/event_analyzer.go:buildInvestigationEvidenceContext` bounded errors and raw logs, but not insights or pattern text; the caller then head-truncated the entire block. | Reserve separate insight, pattern, error, metric, and raw-log budgets. Preserve truncation markers and both ends of errors. |
| Some truncated logs had no full-text copy | Inline logs used the budget remaining after other evidence, but `saveEvidenceLogsToWorkspace` only saved logs above 12 KB. | Use the same 1 KB raw-log threshold for inline sampling and workspace offload. Structured evidence already has a separate full-copy path. |
| A namesake file could supply the fixer's evidence | `filterCitationsToFiles` matched only basenames, so `worker/redis.go` could corroborate a plan targeting `gateway/redis.go`. | Match complete paths or directory-qualified suffixes. A bare filename requires an exact match; ambiguous citations are omitted so source can be checked again. |
| Malformed plans could invoke expensive fixing | Unknown plan kinds defaulted to source; instructions-only and files-only plans were accepted. | Preserve unknown kinds and validate kind, instructions, and concrete repo-relative paths before execution, including recovered plans. Existing valid stored plans need no migration. |
| Optional hardening could become an incident fix | `buildCodeCapabilityBlock` explicitly requested source exploration for hardening even when the cause was external. | Require incident evidence supporting a defect; keep optional hardening in prose. Include causal evidence and a focused regression check in the existing `change` handoff field. |
| Missing evidence could be mistaken for benign behavior | The investigation prompt treated a directly posted alert with no matching rule as a manual/test post. Its resource-only filter also conflicted with dependency investigation. | Require affirmative provenance for test classification; distinguish incomplete evidence, benign positives, and false positives; admit verified causal dependencies. |

These changes affect the common event prompt builder and handoff, so both ReAct4
and the ReAct3 fallback receive them. They do not change triage scoring, suppress
events automatically, or establish semantic correctness from a valid plan.

## Remaining limitations, in priority order

1. **Preserve a typed causal handoff.** The fixer gets change instructions,
   located files, and source citations through `buildFixModeQuery`; it does not
   receive a typed record of the incident's observations, rejected hypotheses,
   coverage, deployed revision, or expected behavioral change. The prompt now
   requests evidence and a regression check in the existing field, but this is
   still model-authored prose. Next: version a bounded handoff containing those
   fields, with source references to actual successful tool results. Abstain
   when the causal link is missing; do not gate on a self-reported confidence
   number alone. Preserve legacy records as unverified rather than inventing
   evidence for them.
2. **Measure behavioral verification separately from build verification.**
   `llm/code-analysis/agents/fix_verifier.go` runs discovered build commands
   (including scoped lint for Python). This provides useful deterministic
   evidence, but does not prove the incident is fixed. Next: run a targeted
   regression that reproduces the observed failure before the patch and passes
   after it, when a safe reproducible fixture exists. Keep `build_verified`,
   `behavior_verified`, and `incident_resolved` separate. Missing dependencies
   or unavailable runtime evidence mean unverified, not success.
3. **Turn evidence coverage into data.** The current coverage inventory is
   inferred by the model from digests and workspace files. Store subject,
   interval, source, query, collection time, completeness, and retrieval error
   alongside each evidence item. Cache/reuse only equivalent subject + interval
   + query + revision; preserve errors and negative evidence. On workspace-save
   failure, explicitly report that the full payload is unavailable. Do not
   mark an entire telemetry category covered because one sample exists.
4. **Replay before broad rollout.** Embedded prompt changes can be superseded
   by account/global/experiment overrides (`prompts/loader.go`); record the
   effective prompt source/version and model for comparisons. Verify that the
   candidate prompt actually resolves for the test cohort. Use the existing
   prompt experiment mechanism for prompt-only changes; deterministic code
   guards are normal service changes and are not isolated by a prompt experiment.
5. **Keep triage priority separate from truth.** The triage subsystem already
   has classifications, human overrides, cached signal verdicts, and fallback
   scoring. Reuse those as context, not causal proof. A low priority or closed
   event is not evidence that the condition was false. Suppression proposals
   need a scoped rule and historical dry-run; track real incidents incorrectly
   suppressed as well as nuisance alerts removed.

## Evaluation contract

Use a frozen, reviewed set of incident snapshots, split by incident family so
duplicates do not leak between development and holdout sets. Include real code
defects, deployment configuration faults, intentional shutdowns, external
dependency failures, chronic signals, missing/expired telemetry, same-basename
source files, and direct alert posts with both real and test payloads. Replays
must use recorded incident-time evidence; today's healthy state cannot answer
what happened then. Run candidate and baseline with the same model, budgets,
tool fixtures, repository revision, and effective prompt version.

Report these independently, with denominators and paired case-level outcomes:

- Fix precision: verified correct fixes / all proposed fixes.
- Fix recall: correct fixes proposed / cases with a known actionable fix.
- False suppression: real incidents classified for suppression / real incidents.
- Unsupported cause rate: conclusions lacking causal evidence / all conclusions.
- Abstention quality: unknown verdicts on insufficient evidence, plus missed
  diagnosable cases. Do not improve precision simply by returning no fixes.
- Efficiency: p50/p95 tokens, cost, latency, duplicate equivalent retrievals,
  source-analysis invocations, and cost per verified correct fix.

Start with paired offline replay, then a small account-scoped shadow cohort.
Review disagreements without showing the evaluator which output is the
candidate. Expand only after precision improves without unacceptable recall or
false-suppression regressions. No measured ratio improvement is claimed by the
unit tests in this patch: they establish the deterministic failure cases above.

## Local verification

Run `make validate` from `llm/llm-server`. Focused regression cases live in
`api/event_analyzer_evidence_test.go` and `api/event_analyzer_change_plan_test.go`;
the existing prompt suite checks template rendering. Live LLM replay and
post-deployment incident verification remain separate from these checks.

Validation on this checkout: focused API/prompt regressions passed. `make
validate` passed lint and the API/prompt race tests, but failed in the existing
`common` RabbitMQ tests because localhost:5672 reset the connection; those tests
then dereferenced a nil publisher. No live model replay or deployment was run.
