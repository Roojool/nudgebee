package planners

import "testing"

// TestInjectSecureContext_CliBackgroundGetsWorkingDirectory guards against a
// regression where cli_background silently fell back to the pre-clone
// workspace root: it was missing from the action list that receives the
// planner-injected cloned repository path, so a background command with no
// explicit working_dir ran in the wrong place.
func TestInjectSecureContext_CliBackgroundGetsWorkingDirectory(t *testing.T) {
	p := NewReActPlanner(nil, nil, 10)
	p.SetSecureContext("working_directory", "/workspace/cloned-repo")

	result := p.injectSecureContext("cli_background", map[string]any{
		"action":  "start",
		"command": "npm run dev",
	})

	if got, ok := result["working_directory"]; !ok || got != "/workspace/cloned-repo" {
		t.Fatalf("expected working_directory to be injected for cli_background, got: %+v", result)
	}
}
