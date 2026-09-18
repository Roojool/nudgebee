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
	"path/filepath"
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

	// maxInlineProfileBytes bounds what we hand back inside the task output.
	// The profile has to come back for anything downstream to use it — the
	// evidence card, an LLM task, a ticket attachment — but a jcmd heapdump is
	// a whole JVM heap and a Node heapsnapshot is the same class, and those
	// would blow Temporal's 2MB payload ceiling. 256KB is Temporal's own
	// warning threshold, so past it we return the handle and say so rather
	// than killing the run.
	maxInlineProfileBytes = 256 * 1024
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

// awaitProfile polls until the agent reports a terminal state, then returns the
// profile itself so the rest of the workflow can use it.
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

// profileResult is the task output: what was captured, and the artefact itself
// unless it is too large to carry.
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
	if profile := profileDetails(status); profile != nil {
		for _, key := range []string{"lang", "profile_tool", "filename"} {
			if v, ok := profile[key].(string); ok && v != "" {
				result[key] = v
			}
		}
		filename, _ := profile["filename"].(string)
		if contents, ok := profile["contents_base64"].(string); ok && contents != "" {
			size, sized := base64DecodedSize(contents)
			if sized {
				result["size_bytes"] = size
			}
			switch {
			case sized && size <= maxInlineProfileBytes:
				// Shaped for the Investigate page's profile card, which keys
				// off the file type and extension rather than an action name
				// (SVGImageCard). events.add_evidence can pass this straight
				// through, and anything else downstream gets the bytes.
				result["file"] = map[string]any{
					"filename": filename,
					"type":     profileFileType(filename),
					"data":     contents,
				}
			default:
				result["contents_omitted"] = fmt.Sprintf(
					"the profile is %s, over the %s a task output can carry — fetch it from the pod's Profiler tab, or ask for a shorter profile_duration",
					describeSize(size, sized), describeSize(maxInlineProfileBytes, true))
			}
		}
	}
	return result
}

// profileFileType is the evidence "type" the profile card matches on. The
// agent names its artefacts by what they are — …flamegraph-1.html.gz,
// …heapdump-1.pprof.gz — so the final extension is the discriminator.
func profileFileType(filename string) string {
	ext := strings.TrimPrefix(filepath.Ext(filename), ".")
	if ext == "" {
		return "gz"
	}
	return ext
}

func describeSize(size int, known bool) string {
	if !known {
		return "an unreadable size"
	}
	return fmt.Sprintf("%dKB", size/1024)
}

// profileDetails digs out the block the agent itself produced. services-server
// wraps it twice on the way back — base64_profile holds the agent's response,
// whose own `data` is the file block — so reading base64_profile directly finds
// only the wrapper and silently reports no language, tool or size. Descend
// while `data` is another map, stopping as soon as the fields appear, so this
// keeps working if a layer is ever flattened.
func profileDetails(status map[string]any) map[string]any {
	node, ok := status["base64_profile"].(map[string]any)
	if !ok {
		return nil
	}
	for range 3 {
		if isProfileBlock(node) {
			return node
		}
		inner, ok := node["data"].(map[string]any)
		if !ok {
			break
		}
		node = inner
	}
	// Never hand back a wrapper: the caller would read nothing out of it and
	// report a profile with no language, tool or bytes — the failure this
	// function exists to prevent.
	return nil
}

// isProfileBlock recognises the agent's file block by the two fields the
// caller actually consumes, so a block is still usable if one of them is
// missing. The wrappers around it carry neither.
func isProfileBlock(node map[string]any) bool {
	for _, key := range []string{"filename", "contents_base64"} {
		if v, ok := node[key].(string); ok && v != "" {
			return true
		}
	}
	return false
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
			"file": {
				Type:        types.PropertyTypeObject,
				Description: "The profile itself: filename, type and base64 data, shaped for events.add_evidence so the Investigate page renders it as a profile card.",
			},
			"contents_omitted": {
				Type:        types.PropertyTypeString,
				Description: "Set instead of file when the profile is too large to carry in a task output, explaining where to get it.",
			},
		},
	}
}
