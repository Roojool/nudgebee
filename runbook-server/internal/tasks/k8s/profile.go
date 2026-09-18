package k8s

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"nudgebee/runbook/common"
	"nudgebee/runbook/config"
	"nudgebee/runbook/internal/tasks/types"
	"strconv"
	"strings"
	"time"
)

// pod_profiler is not a light action on the agent — it spawns a privileged
// debugger pod — so it cannot be dispatched over the relay the way
// k8s.continuous_rightsize is. It goes the same route the profiler screen
// uses: services-server queues an agent_task and the agent drains it.
const (
	profileSubmitAction = "applications_execute_profile"
	profileStatusAction = "applications_get_profile_status"

	// Headroom over the requested duration for the queue tick, the debugger
	// pod starting, and the result being copied back out. The task's own
	// `timeout:` in the workflow definition still has the final say.
	profileOverhead = 5 * time.Minute

	defaultProfileDuration = 60
	maxProfileDuration     = 600
)

// profilePollInterval is how often we ask services-server whether the agent is
// done. The agent drains its task queue on a 2-minute tick, so a profile is
// never ready immediately; poll patiently rather than often. A var so tests do
// not have to wait it out.
var profilePollInterval = 10 * time.Second

type ProfileTask struct{}

func (t *ProfileTask) GetName() string {
	return "k8s.profile"
}

func (t *ProfileTask) GetDescription() string {
	return "Capture a CPU or memory profile from a running pod. The profiler picks the right tool for " +
		"the application (pprof for Go, async-profiler or jcmd for Java, py-spy for Python, rbspy for Ruby) " +
		"and the finished profile is kept with the pod, on its Profiler tab."
}

func (t *ProfileTask) GetDisplayName() string {
	return "Pod Profile"
}

func (t *ProfileTask) Execute(taskCtx types.TaskContext, params map[string]any) (any, error) {
	taskCtx.GetLogger().Debug("Executing ProfileTask", "params", params)

	accountID := taskCtx.GetAccountID()
	if id, ok := params["account_id"].(string); ok && id != "" {
		accountID = id
	}

	namespace, _ := params["namespace"].(string)
	podName, _ := params["pod_name"].(string)
	if namespace == "" || podName == "" {
		return nil, errors.New("namespace and pod_name are required")
	}
	if !k8sNameRegex.MatchString(namespace) {
		return nil, fmt.Errorf("invalid namespace format: %s", namespace)
	}
	if !k8sNameRegex.MatchString(podName) {
		return nil, fmt.Errorf("invalid pod_name format: %s", podName)
	}

	profileType, _ := params["profile_type"].(string)
	if profileType == "" {
		profileType = "cpu"
	}
	if profileType != "cpu" && profileType != "memory" {
		return nil, fmt.Errorf("profile_type must be cpu or memory, got %q", profileType)
	}

	duration := defaultProfileDuration
	if d, ok := toInt(params["profile_duration"]); ok && d > 0 {
		duration = d
	}
	if duration > maxProfileDuration {
		return nil, fmt.Errorf("profile_duration must be at most %ds, got %ds", maxProfileDuration, duration)
	}

	request := map[string]any{
		"account_id":       accountID,
		"namespace":        namespace,
		"pod_name":         podName,
		"profile_type":     profileType,
		"profile_duration": duration,
	}
	// All optional: the agent resolves the language from the node-agent's
	// container_application_type metric when we do not name one, and derives
	// the tool and output from (language, profile_type).
	for _, key := range []string{"application_language", "profile_tool", "output_type"} {
		if v, ok := params[key].(string); ok && v != "" {
			request[key] = v
		}
	}

	if taskCtx.IsDryRun() {
		return map[string]any{
			"dry_run": true,
			"request": request,
		}, nil
	}

	submitted, err := callProfileAction(taskCtx, profileSubmitAction, request)
	if err != nil {
		return nil, fmt.Errorf("failed to start the profile: %w", err)
	}
	profileID, _ := submitted["profile_task_id"].(string)
	if profileID == "" {
		return nil, errors.New("the profiler did not return a profile id")
	}
	taskCtx.GetLogger().Info("Profile queued", "profile_task_id", profileID, "pod", podName, "namespace", namespace)

	return t.awaitProfile(taskCtx, accountID, profileID, duration, request)
}

// awaitProfile polls until the agent reports a terminal state. It deliberately
// returns metadata only: a flamegraph runs to hundreds of kilobytes, and
// services-server already stores the artefact against the pod, so carrying it
// through the workflow payload would buy nothing.
func (t *ProfileTask) awaitProfile(
	taskCtx types.TaskContext,
	accountID, profileID string,
	duration int,
	request map[string]any,
) (any, error) {
	deadline := time.Now().Add(time.Duration(duration)*time.Second + profileOverhead)
	ctx := taskCtx.GetContext()

	for {
		status, err := callProfileAction(taskCtx, profileStatusAction, map[string]any{
			"account_id": accountID,
			"profile_id": profileID,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to read the profile status: %w", err)
		}

		switch state, _ := status["status"].(string); state {
		case "COMPLETED":
			return profileResult(profileID, request, status), nil
		case "FAILED", "TIMEOUT":
			message, _ := status["message"].(string)
			if message == "" {
				message = state
			}
			return nil, fmt.Errorf("the profile did not complete: %s", message)
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf(
				"the profile was still running after %s — raise this task's timeout, or ask for a shorter profile_duration",
				time.Duration(duration)*time.Second+profileOverhead)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(profilePollInterval):
		}
	}
}

// profileResult is the task output: what was captured and where to find it,
// without the artefact itself.
func profileResult(profileID string, request map[string]any, status map[string]any) map[string]any {
	result := map[string]any{
		"profile_task_id": profileID,
		"status":          "COMPLETED",
		"namespace":       request["namespace"],
		"pod_name":        request["pod_name"],
		"profile_type":    request["profile_type"],
	}
	// The agent reports what it actually did, which is not always what was
	// asked for — the language is auto-detected and picks the tool.
	if profile, ok := status["base64_profile"].(map[string]any); ok {
		for _, key := range []string{"lang", "profile_tool", "filename"} {
			if v, ok := profile[key].(string); ok && v != "" {
				result[key] = v
			}
		}
		if contents, ok := profile["contents_base64"].(string); ok {
			if size, ok := base64DecodedSize(contents); ok {
				result["size_bytes"] = size
			}
		}
	}
	return result
}

// base64DecodedSize is the artefact's own size in bytes — the gzipped profile
// the agent produced. Reporting len() of the encoded string instead would
// overstate it by a third. Returns false rather than a guess when the payload
// is not well-formed base64.
func base64DecodedSize(encoded string) (int, bool) {
	if encoded == "" || len(encoded)%4 != 0 {
		return 0, false
	}
	return base64.StdEncoding.DecodedLen(len(encoded)) - strings.Count(encoded[len(encoded)-2:], "="), true
}

// callProfileAction posts one RPC action envelope to services-server, the same
// shape the profiler screen sends.
func callProfileAction(taskCtx types.TaskContext, action string, request map[string]any) (map[string]any, error) {
	requestContext := taskCtx.GetNewRequestContext()

	resp, err := common.HttpPost(
		fmt.Sprintf("%s/rpc/application", strings.TrimRight(config.Config.ServiceEndpoint, "/")),
		common.HttpWithHeaders(map[string]string{
			"Content-Type":   "application/json",
			"Accept":         "application/json",
			"X-ACTION-TOKEN": config.Config.ServiceApiServerToken,
			"x-tenant-id":    requestContext.GetSecurityContext().GetTenantId(),
			"x-user-id":      requestContext.GetSecurityContext().GetUserId(),
		}),
		common.HttpWithJsonBody(map[string]any{
			"action": map[string]any{"name": action},
			"input":  map[string]any{"request": request},
		}),
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resp.Body != nil {
			if closeErr := resp.Body.Close(); closeErr != nil {
				taskCtx.GetLogger().Debug("failed to close response body", "error", closeErr)
			}
		}
	}()

	jsonBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("%s returned %d: %s", action, resp.StatusCode, strings.TrimSpace(string(jsonBody)))
	}

	var body map[string]any
	if err := common.UnmarshalJson(jsonBody, &body); err != nil {
		return nil, err
	}
	// Action errors come back 200 with a message rather than a data block.
	if message, ok := body["message"].(string); ok && message != "" {
		return nil, errors.New(message)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected response from %s: %s", action, strings.TrimSpace(string(jsonBody)))
	}
	return data, nil
}

// toInt accepts the shapes a workflow parameter can arrive in: JSON numbers
// decode to float64, templated values arrive as strings.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

func (t *ProfileTask) InputSchema() *types.Schema {
	return &types.Schema{
		Properties: map[string]types.Property{
			"account_id": {
				Type:  types.PropertyTypeAccount,
				Title: "Account",
				Order: 1,
			},
			"namespace": {
				Type:      types.PropertyTypeString,
				Required:  true,
				Title:     "Namespace",
				Order:     2,
				DependsOn: []string{"account_id"},
			},
			"pod_name": {
				Type:        types.PropertyTypeString,
				Required:    true,
				Description: "Pod to profile.",
				Title:       "Pod",
				Order:       3,
				DependsOn:   []string{"account_id", "namespace"},
			},
			"profile_type": {
				Type:        types.PropertyTypeString,
				Required:    false,
				Default:     "cpu",
				Description: "What to sample. CPU produces a flamegraph; memory produces a heap profile or histogram, depending on the language.",
				Title:       "Profile Type",
				Order:       4,
				Options:     []string{"cpu", "memory"},
			},
			"profile_duration": {
				Type:        types.PropertyTypeInteger,
				Required:    false,
				Default:     defaultProfileDuration,
				Description: "Seconds to sample for, up to 600. Give this task a timeout of at least the duration plus five minutes.",
				Title:       "Duration (seconds)",
				Order:       5,
			},
			"application_language": {
				Type:        types.PropertyTypeString,
				Required:    false,
				Description: "Leave empty to let the profiler detect it. Set it only when detection has no signal for the pod.",
				Title:       "Language",
				Order:       6,
				Options:     []string{"go", "java", "python", "node", "ruby"},
			},
		},
	}
}

func (t *ProfileTask) OutputSchema() *types.Schema {
	return &types.Schema{
		Properties: map[string]types.Property{
			"status":          {Type: types.PropertyTypeString},
			"profile_task_id": {Type: types.PropertyTypeString},
			"namespace":       {Type: types.PropertyTypeString},
			"pod_name":        {Type: types.PropertyTypeString},
			"profile_type":    {Type: types.PropertyTypeString},
			"profile_tool":    {Type: types.PropertyTypeString},
			"lang":            {Type: types.PropertyTypeString},
			"filename":        {Type: types.PropertyTypeString},
			"size_bytes":      {Type: types.PropertyTypeInteger},
		},
	}
}
