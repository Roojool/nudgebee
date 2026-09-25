# VM fleet orchestrator: target selection proposal

**Status:** Draft implementation rebased onto the merged VM SSH backend, 2026-09-25. Local dev-backed inventory and read-only investigation tests passed; deployed UI routing and write approval remain open.

## Goal and acceptance evidence

An investigation started from an account with `account_type = 'vm'` selects `vm_orchestrator`. It can inspect one named VM or a bounded set of VMs in that account, and every remote observation identifies the VM resource, address, SSH datasource, command, exit code, and collection time. An unknown, ambiguous, unreachable, or unauthorized target produces a visible per-target failure; it cannot become evidence of a healthy VM.

## Existing contracts

- `GetDebugAgentName` selects orchestrators from `cloud_provider` and falls back to Kubernetes. VM account type is not considered (`llm/llm-server/agents/agent_debug.go`).
- The planner injects `shell_execute` into most agents. It operates in the conversation workspace; the orchestrator can keep it for local aggregation (`agents/core/utils.go`, `tools/tool_shell.go`).
- `server_command_executor` now resolves discovered VM targets for dynamic SSH integrations. Its `vm_agent` relay path accepts `host_name` and forwards it as `params.host` (`tools/tool_server.go`, `tools/server_vm_target.go`, `tools/common_relay.go`).
- Forager's `ssh-proxy` dynamic mode accepts `params.host` and checks `allowed_hosts`. An empty allowlist permits every host, so that check cannot be the only account boundary (`/Users/shiv/Workspace/forager/pkg/proxy/ssh/proxy.go`).
- Auto-registered proxy datasources receive an `integrations_cloud_accounts` link with `link_role='own'`; forager advertises dynamic SSH mode and `allowed_hosts` in datasource metadata, which relay-server persists in `integrations.labels` (`collector-server/k8s-collector/relay-server/pkg/db/db_store.go`).
- VM inventory lives in `cloud_resourses`. The VM scan path verifies a resource against account and tenant and resolves an address from `vm-<ip>` or resource metadata (`api-server/services/vmpackage/service.go`, `resource_match.go`). Discovery datasource links express scanning ownership; they do not identify SSH credentials (`api-server/services/vmpackage/cron.go`).

## Proposed v1 selection rule

1. **Account:** Use `account_type = 'vm'` to choose `vm_orchestrator`; retain the account ID as the execution scope. Validate that the account is active and belongs to the caller before tool execution. An explicit mention of another agent remains an explicit route, subject to that agent's authorization.
2. **VM:** Resolve a name, resource ID, or user-selected VM to an active `cloud_resourses` row in the selected tenant and account. If the question addresses a fleet, enumerate that account's active VM rows and apply an explicit result/call limit. Reject a name matching multiple rows; do not select the first one.
3. **Address:** Resolve a current IP from the verified row. Prefer an address reachable from the selected datasource; never substitute a hostname or IP supplied only in the question. Missing or competing addresses require a visible choice or a verified source update. Recheck resource membership and address at execution time rather than trusting a previous planner step.
4. **SSH datasource:** Consider only enabled `ssh` integrations linked to the selected account, in `connection_mode=vm_agent`, with a reachable `ssh-proxy` datasource and explicit nonempty `allowed_hosts`. The selected datasource must permit the resolved IP. If exactly one qualifies, use it. If none or several qualify, return a target-specific configuration/ambiguity result; do not probe each datasource or choose by list order. A future persisted VM-to-SSH-datasource binding can resolve legitimate overlap.
5. **Execution:** Pass the verified address as `params.host` with `datasource_id`, `command`, and timeout to forager `ssh_command`. Preserve the normal read/write approval classification for each command. Return target provenance and stdout/stderr/exit code. Apply bounded concurrency and per-target timeouts for fleet calls; partial failure stays partial.

This suggests a narrow target-aware VM execution wrapper around the existing relay path. It may reuse `server_command_executor` internally, but its public VM input should be a resource ID and command, not a model-selected integration name and arbitrary host override. Keep `shell_execute` for workspace files and aggregation. Translating workspace `ssh` into this wrapper is optional follow-up after the direct path works.

## Deferred in v1

Generic logs, metrics, traces, and event agents are mounted only after verifying that their provider resolves VM account data and VM identity correctly. Local `journalctl` or bounded file reads can be requested through the VM execution path. Recommendations can be exposed after checking their account and VM resource filters. No Kubernetes telemetry fallback is treated as VM evidence.

## Adversarial review

1. **Inventory can be stale.** Recheck membership and address immediately before execution; return the time and identity used. A live host response alone does not prove that the address still belongs to the inventoried VM.
2. **Network scope is not account scope.** A forager CIDR or `allowed_hosts` entry may include machines outside this VM account. Require the inventory check and explicit SSH datasource account link. Empty allowlists are ineligible for automated VM investigation.
3. **Credential overlap can be real.** Multiple SSH datasources may cover one host. A deterministic ordering would hide which credential was used; fail with the eligible datasource identities until an operator chooses or persists a binding.

**Verdict: PROCEED with the narrow implementation.** The forager supports per-host SSH, and relay-server persists its scope metadata with an account link. `vm_execute` reads that JSON at execution time, requires a nonempty allowlist, and passes the verified IP to forager. The metadata is a reported snapshot and may be stale; forager independently checks its current allowlist. Do not infer SSH authority from discovery datasource association.

## Implementation state

The worktree adds `vm_orchestrator`, `vm_list_execute`, and `vm_execute`. Routing uses `account_type = 'vm'`. The VM tools include generic `VM` rows and AWS `compute-instance` rows matched into the VM account by discovery. `vm_execute` rechecks the account and VM, uses account-linked dynamic SSH metadata, and returns selected-target provenance with stdout, stderr, and exit code. It chooses a private inventory address first, then a public one if no eligible datasource covers the private address. Multiple eligible datasources for the chosen address require an explicit datasource ID. It passes the selected IP as `host_name` to the merged SSH backend, which forwards it as `params.host` to forager. Both VM and existing server SSH commands use the command intent classifier and bind write confirmation to the exact tool input, so a later command or target needs a new approval. Generic logs, metrics, traces, and events remain deferred.

The inventory-to-IP mapping is not a cryptographic proof of the host answering at that address. A stale IP can point at another machine; the result therefore identifies the selected inventory target and address, not an independently attested host identity. A future host-identity check should precede any unattended mutating operation.

## Verification scenarios

Initial dev-backed tests on 2026-09-25 used the draft llm-server locally with the dev database and relay. `vm_list_execute` returned eight account VMs. `vm_execute` selected an account-linked SSH datasource and ran read-only `uname -s` on an inventoried Linux VM; an unknown resource ID was rejected before remote execution. Account routing, prompt loading, and both VM tool registrations passed. A read-only `vm_orchestrator` conversation completed and returned the eight-VM inventory. These tests are opt-in under the `e2e` build tag in `tool_vm_e2e_test.go` and `agent_vm_orchestrator_e2e_test.go`. The dev llm-server deployment still runs `main`, so UI routing of this draft is not yet tested. Local workflow and RAG endpoints were unavailable during the conversation; their existing fallbacks did not block the inventory response.

The exact query `List the VMs in this account` initially produced a plausible eight-VM answer without a verifiable inventory tool step. The prompt now explicitly requires a current-turn `vm_list_execute` call for inventory questions. A repeat local run completed with `tools=[vm_list_execute]` and no remote SSH invocation; the opt-in conversation test checks both conditions.

Six additional local conversations exercised two-VM disk and inode comparison, SSH service health, CPU processes, current memory and OOM evidence, DNS and HTTPS reachability, and two-VM OS/kernel drift. Each selected VMs through `vm_list_execute`; successful remote calls returned separate target evidence. The SSH user could read service status but lacked journal access, so historical service and OOM conclusions remain limited. The first memory conversation made eight remote probes, including redundant privileged attempts. Prompt guidance now asks for separate small probes and one clear report of inaccessible logs; a retest reduced memory collection to four probes. A service retest used separate commands: `systemctl status sshd --no-pager` exited 0, `journalctl -u sshd -n 20 --no-pager` exited 1, and the answer called health partially verified. These are local draft llm-server runs, not deployed dev UI validation.

- One VM, one eligible SSH datasource: correct orchestrator, exact VM address, successful command, complete provenance.
- Two VMs in one account: bounded calls to both, separate results and failures.
- Unknown/duplicate VM, stale IP, disabled or disconnected datasource, empty allowlist, and two eligible datasources: no remote command until the scope is resolved.
- VM in another account or IP within the datasource CIDR but absent from account inventory: rejected before relay execution.
- A mutating command: existing approval flow applies to the exact VM and command.
