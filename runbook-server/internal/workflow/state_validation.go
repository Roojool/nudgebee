package workflow

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"nudgebee/runbook/internal/model"
)

var (
	varRefPattern   = regexp.MustCompile(`Vars(?:\.([A-Za-z0-9_-]+)|\[\s*['"]([^'"]+)['"]\s*\])`)
	stateRefPattern = regexp.MustCompile(`State(?:\.([A-Za-z0-9_-]+)|\[\s*['"]([^'"]+)['"]\s*\])`)
)

// ValidateNoSecretsInState rejects a definition whose set_state writes a
// credential — directly, or through a var or state key that holds one. Output
// of a step that used a secret (a fetched token, say) is the author's to keep.
func ValidateNoSecretsInState(tasks []model.Task) error {
	var all []model.Task
	var flatten func([]model.Task)
	flatten = func(ts []model.Task) {
		for _, t := range ts {
			all = append(all, t)
			flatten(t.Tasks)
			flatten(extractSubtasksFromParams(t.Params))
		}
	}
	flatten(tasks)

	secretVars, secretState := map[string]bool{}, map[string]bool{}
	carries := func(k string, v any) bool {
		return containsSecretReference(k) || containsSecretReference(v) ||
			readsAny(k, v, varRefPattern, secretVars) || readsAny(k, v, stateRefPattern, secretState)
	}

	// Mark keys holding a credential until nothing changes; each pass adds a key or stops.
	for changed := true; changed; {
		changed = false
		for _, t := range all {
			for k, v := range t.SetVars {
				if !secretVars[k] && carries(k, v) {
					secretVars[k], changed = true, true
				}
			}
			for k, v := range t.SetState {
				if !secretState[k] && carries(k, v) {
					secretState[k], changed = true, true
				}
			}
		}
	}

	var offending []string
	for _, t := range all {
		for k, v := range t.SetState {
			if carries(k, v) {
				offending = append(offending, t.ID+"."+k)
			}
		}
	}
	if len(offending) == 0 {
		return nil
	}
	sort.Strings(offending)
	return fmt.Errorf("set_state cannot store a credential (%s): read it from Secrets in the step that needs it instead", strings.Join(offending, ", "))
}

// readsAny reports whether key or value references a name in names via pattern.
func readsAny(key string, value any, pattern *regexp.Regexp, names map[string]bool) bool {
	if len(names) == 0 {
		return false
	}
	var found bool
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			for _, m := range pattern.FindAllStringSubmatch(t, -1) {
				if names[m[1]] || names[m[2]] {
					found = true
				}
			}
		case map[string]any:
			for k, val := range t {
				walk(k)
				walk(val)
			}
		case []any:
			for _, val := range t {
				walk(val)
			}
		}
	}
	walk(key)
	walk(value)
	return found
}
