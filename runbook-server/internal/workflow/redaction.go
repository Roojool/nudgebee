package workflow

import (
	"net/url"
	"nudgebee/runbook/internal/model"
	"regexp"
	"sort"
	"strings"
)

const RedactedValue = "***REDACTED***"

// ScrubbedValue replaces a credential's literal value inside free text.
const ScrubbedValue = "***"

// minScrubLength keeps short values such as "true" or "8080" from blanking unrelated text.
const minScrubLength = 6

// secretRefPattern matches template references to Secrets in both Jinja and Go template syntax.
// Examples: {{ Secrets['key'] }}, {{ Secrets.key }}, {{.Secrets.key}}
var secretRefPattern = regexp.MustCompile(`Secrets[\[.\s]`)

// containsSecretReference recursively checks if a value contains
// a template reference to Secrets.
func containsSecretReference(value any) bool {
	switch v := value.(type) {
	case string:
		return secretRefPattern.MatchString(v)
	case map[string]any:
		for _, val := range v {
			if containsSecretReference(val) {
				return true
			}
		}
	case []any:
		for _, val := range v {
			if containsSecretReference(val) {
				return true
			}
		}
	}
	return false
}

// buildTaskDefinitionMap builds a flat map of taskID -> *model.Task from
// the recursive workflow definition task tree.
func buildTaskDefinitionMap(tasks []model.Task) map[string]*model.Task {
	m := make(map[string]*model.Task)
	var build func(tasks []model.Task)
	build = func(tasks []model.Task) {
		for i := range tasks {
			task := &tasks[i]
			// A container may list a top-level task by bare id; keep the full definition.
			if existing, ok := m[task.ID]; !ok || existing.Type == "" {
				m[task.ID] = task
			}
			if len(task.Tasks) > 0 {
				build(task.Tasks)
			}
			// core.group / core.foreach keep their children in params["tasks"].
			if subtasks := extractSubtasksFromParams(task.Params); len(subtasks) > 0 {
				build(subtasks)
			}
		}
	}
	build(tasks)
	return m
}

// collectSecretParamKeys records, by name, every key whose leaf value references Secrets.
// Returns true when a reference sits where no key can address it, so the caller blanks the container.
func collectSecretParamKeys(value any, out map[string]bool) bool {
	switch v := value.(type) {
	case map[string]any:
		unaddressable := false
		for k, val := range v {
			if containsSecretReference(k) { // the resolved key is the secret
				unaddressable = true
			}
			switch val.(type) {
			case map[string]any, []any:
				if collectSecretParamKeys(val, out) {
					out[k] = true
				}
			default:
				if containsSecretReference(val) {
					out[k] = true
				}
			}
		}
		return unaddressable
	case []any:
		unaddressable := false
		for _, val := range v {
			switch val.(type) {
			case map[string]any, []any:
				if collectSecretParamKeys(val, out) {
					unaddressable = true
				}
			default:
				// a bare element has no key of its own
				if containsSecretReference(val) {
					unaddressable = true
				}
			}
		}
		return unaddressable
	}
	return false
}

// buildSecretParamKeys identifies which param keys in each task definition
// contain references to Secrets, at any nesting depth.
// Returns taskID -> set of param keys.
func buildSecretParamKeys(taskDefs map[string]*model.Task) map[string]map[string]bool {
	result := make(map[string]map[string]bool)
	for taskID, task := range taskDefs {
		if task.Params == nil {
			continue
		}
		secretKeys := make(map[string]bool)
		// A secret as a top-level param key has no container to blank; only value
		// scrubbing reaches it, and allowedParams validation should prevent the shape.
		_ = collectSecretParamKeys(task.Params, secretKeys)
		if len(secretKeys) > 0 {
			result[taskID] = secretKeys
		}
	}
	return result
}

// redactDeep copies value, blanking secret-keyed fields through maps and slices.
// Copy-on-write is load-bearing: synthesized and skipped task Inputs alias the definition.
func redactDeep(value any, secretKeys map[string]bool) any {
	switch v := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(v))
		for k, val := range v {
			if secretKeys[k] {
				redacted[k] = RedactedValue
				continue
			}
			redacted[k] = redactDeep(val, secretKeys)
		}
		return redacted
	case []any:
		redacted := make([]any, len(v))
		for i, val := range v {
			redacted[i] = redactDeep(val, secretKeys)
		}
		return redacted
	default:
		return value
	}
}

// redactTaskParams blanks secret-containing fields at any depth, returning a fresh copy.
func redactTaskParams(params map[string]any, secretKeys map[string]bool) map[string]any {
	if len(secretKeys) == 0 || params == nil {
		return params
	}
	redacted, ok := redactDeep(params, secretKeys).(map[string]any)
	if !ok {
		return params
	}
	return redacted
}

// resolveSecretKeysForTask finds the secret param keys for a given task ID.
// It handles foreach-prefixed IDs where the Temporal task ID is
// "{foreachID}-{index}-{childID}" but the definition only has "{childID}".
func resolveSecretKeysForTask(taskID string, secretParamKeys map[string]map[string]bool) map[string]bool {
	// Direct match
	if keys, ok := secretParamKeys[taskID]; ok {
		return keys
	}
	// Foreach child pattern: check if any defined task ID is a suffix of taskID
	for defID, keys := range secretParamKeys {
		suffix := "-" + defID
		if strings.HasSuffix(taskID, suffix) {
			return keys
		}
	}
	return nil
}

// RedactSecretsFromTasks blanks resolved secrets in task Input and RenderedParams.
// RenderedParams matters because ExecutionsView renders `rendered_params ?? input`.
func RedactSecretsFromTasks(tasks []model.TaskExecutionDetails, wfDef model.WorkflowDefinition) {
	taskDefs := buildTaskDefinitionMap(wfDef.Tasks)
	secretParamKeys := buildSecretParamKeys(taskDefs)

	if len(secretParamKeys) == 0 {
		return
	}

	var redactRecursive func(tasks []model.TaskExecutionDetails)
	redactRecursive = func(tasks []model.TaskExecutionDetails) {
		for i := range tasks {
			task := &tasks[i]
			if keys := resolveSecretKeysForTask(task.ID, secretParamKeys); keys != nil {
				task.Input = redactTaskParams(task.Input, keys)
				task.RenderedParams = redactTaskParams(task.RenderedParams, keys)
			}
			if len(task.Children) > 0 {
				redactRecursive(task.Children)
			}
		}
	}
	redactRecursive(tasks)
}

// RedactExecutionSecrets redacts a whole execution: params that name Secrets are
// blanked, then every remaining credential value in inputs and errors becomes ***.
// Outputs are left alone: printing a credential there is the author's choice.
func RedactExecutionSecrets(details *model.WorkflowExecutionDetails, wfDef model.WorkflowDefinition, secretValues []string) {
	if details == nil {
		return
	}
	RedactSecretsFromTasks(details.Tasks, wfDef)
	ScrubTaskValues(details.Tasks, secretValues)
	details.Error = ScrubSecretValues(details.Error, secretValues)
}

// ScrubTaskValues replaces credential values in task inputs, rendered params and
// errors, children included. It catches a param that renders a credential without
// naming Secrets, e.g. "{{ Vars.pw }}" after an earlier step stashed one in pw.
func ScrubTaskValues(tasks []model.TaskExecutionDetails, secretValues []string) {
	if len(secretValues) == 0 {
		return
	}
	for i := range tasks {
		task := &tasks[i]
		task.Input, _ = scrubValue(task.Input, secretValues).(map[string]any)
		task.RenderedParams, _ = scrubValue(task.RenderedParams, secretValues).(map[string]any)
		task.Error = ScrubSecretValues(task.Error, secretValues)
		ScrubTaskValues(task.Children, secretValues)
	}
}

// scrubValue returns a copy of v with credential values replaced in every string.
func scrubValue(v any, secretValues []string) any {
	switch t := v.(type) {
	case string:
		return ScrubSecretValues(t, secretValues)
	case map[string]any:
		if t == nil {
			return t
		}
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[ScrubSecretValues(k, secretValues)] = scrubValue(val, secretValues)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = scrubValue(val, secretValues)
		}
		return out
	}
	return v
}

// SecretValues returns the literal forms a secret can take inside text: the raw
// value and its URL-encoded forms, longest first so a secret containing another
// is replaced whole. Values shorter than minScrubLength are skipped.
func SecretValues(secrets map[string]any) []string {
	seen := make(map[string]bool)
	for _, v := range secrets {
		s, ok := v.(string)
		if !ok || len(s) < minScrubLength {
			continue
		}
		seen[s] = true
		seen[url.QueryEscape(s)] = true
		seen[url.PathEscape(s)] = true
	}
	values := make([]string, 0, len(seen))
	for s := range seen {
		values = append(values, s)
	}
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return values[i] < values[j]
	})
	return values
}

// ScrubSecretValues replaces every known credential value in text with ***.
func ScrubSecretValues(text string, secretValues []string) string {
	for _, v := range secretValues {
		text = strings.ReplaceAll(text, v, ScrubbedValue)
	}
	return text
}
