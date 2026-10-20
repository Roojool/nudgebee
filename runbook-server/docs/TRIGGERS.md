# Workflow Triggers

Workflows can be triggered by various events. The `triggers` section in the workflow definition specifies one or more triggers. Each trigger type has its own configuration parameters.

## Supported Triggers

### 1. Schedule Trigger (`schedule`)

Runs the workflow automatically based on a defined schedule (Cron).

#### Parameters (`params`)

*   **`cron`** (string, required): A standard cron expression defining the schedule.
    *   Example: `"0 * * * *"` (Every hour)
    *   Example: `"*/5 * * * *"` (Every 5 minutes)
*   **`overlap_policy`** (string, optional): Defines the behavior when a new schedule run is due while a previous run of the same schedule is still active.
    *   **Supported Values:**
        *   `Skip` (Default): If a run is in progress, the next scheduled run is skipped.
        *   `BufferOne`: The next run is queued and waits for the current one to finish.
        *   `BufferAll`: All missed runs are queued and will execute sequentially.
        *   `AllowAll`: The next run starts immediately, allowing parallel executions.
        *   `CancelOther`: The currently running workflow is canceled, and the new run starts.
        *   `TerminateOther`: The currently running workflow is terminated, and the new run starts.
*   **`catchup_window`** (string, optional): A duration string defining how far back the system looks for missed schedule runs after an outage or pause.
    *   **Format:** Go duration string (e.g., `"10m"`, `"1h"`, `"24h"`).
    *   **Default:** `"60s"`.

#### Example

```yaml
triggers:
  - type: schedule
    params:
      cron: "0 9 * * Mon-Fri" # 9 AM on weekdays
      overlap_policy: BufferOne
      catchup_window: 1h
```

---

### 2. Webhook Trigger (`webhook`)

Runs the workflow when an external HTTP request is received.

#### Parameters (`params`)

*   **`integration_name`** (string, required): The unique identifier of the integration configuration that validates and routes this webhook.

#### Example

```yaml
triggers:
  - type: webhook
          params:
            integration_name: "int-12345-github"
    ```
    
    ## Automatic Context Variables
    
    Every workflow execution automatically includes a set of context variables in the `inputs` map. These can be accessed in your tasks using the `Inputs` prefix (e.g., `{{ Inputs.workflow_execution_time }}`).
    
    | Variable | Description | Format |
    | :--- | :--- | :--- |
    | `workflow_execution_time` | The actual time the current execution started. | RFC3339 |
    | `workflow_scheduled_time` | The intended time the workflow was supposed to run (logical time). | RFC3339 |
    | `workflow_last_execution_time` | The start time of the **previous successful** run of this workflow. | RFC3339 |
    | `workflow_execution_id` | The unique ID (Run ID) of the current execution. | UUID |
    | `workflow_id` | The ID of the workflow definition. | UUID |
    | `workflow_name` | The name of the workflow. | String |
    
    > **Note:** `workflow_last_execution_time` is persisted in the workflow's state and will be empty on the first run. it is only updated upon successful completion of a run.
    ---

### 3. Manual Trigger (`manual`)

Allows the workflow to be triggered manually via the API or CLI. This trigger type accepts no parameters.

#### Example

```yaml
triggers:
  - type: manual
```

### 4. Event Trigger (`event`)

Runs the workflow when a Nudgebee event (alert, anomaly, Kubernetes change, …) matches.

#### Parameters (`params`)

*   **`event_type`** (string or list of strings, optional): Event types to listen for. Empty means every type.
*   **`filter`** (string, optional): A Jinja expression evaluated against `event`; the workflow runs when it renders `true`. One of `event_type` or `filter` is required.
*   **`on`** (string, optional): Lifecycle phase to fire on. Default `event.created`.
*   **`accounts`** (list of account IDs, optional): Accounts whose events fire this workflow. Empty means the workflow's own account only. When set, only the listed accounts fire it — list the own account too if it should keep firing. Every listed account must be an account ID in the workflow's tenant, readable by whoever saves it. Triggers are read from the saved draft, so the list takes effect on save, not on publish.

Steps still default to the **workflow's** account. In a workflow that listens to several accounts, set a step's `account_id` to `{{ Inputs.event.cloud_account_id }}` so it acts on the account the event came from.

#### Example

```yaml
triggers:
  - type: event
    params:
      event_type: ["KubePodCrashLooping"]
      accounts: ["<cluster-a-account-id>", "<cluster-b-account-id>"]
```

### 5. Optimization Trigger (`optimization`)

Runs the workflow when a new open recommendation matches.

#### Parameters (`params`)

*   **`categories`**, **`rule_names`** (lists of strings, optional): Recommendation categories / rule names to match.
*   **`clusters`** (list of account IDs, optional): Accounts whose recommendations fire this workflow. Empty means the workflow's own account only. Same access rules as event `accounts`.
*   **`filter`** (string, optional): Extra Jinja expression, ANDed with the above.

At least one parameter is required.
