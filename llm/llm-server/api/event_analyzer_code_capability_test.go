package api

import (
	"testing"

	"nudgebee/llm/services_server"

	"github.com/stretchr/testify/assert"
)

// The 09-20 benchmark's clean-run failures include three llm-gateway runs plus
// R0159 and R0167, all prescribing a change that was already merged. The
// investigation prompt has to ask the question before the recommendation is
// written; the code stage's no_op check runs too late.
func TestBuildCodeCapabilityBlock(t *testing.T) {
	t.Run("no capability mapped keeps the existing stop-there rule", func(t *testing.T) {
		b := buildCodeCapabilityBlock(&services_server.EventCodeCapabilities{}, "nudgebee", "llm-gateway")
		assert.Contains(t, b, "No source repository or deployment values file is mapped")
		assert.NotContains(t, b, "already there")
	})

	t.Run("source with a deployed commit asks whether the change already shipped", func(t *testing.T) {
		b := buildCodeCapabilityBlock(&services_server.EventCodeCapabilities{
			Source: &services_server.SourceCapability{
				Repo:   "nudgebee/nudgebee-enterprise",
				Commit: "cf1eb58e",
			},
		}, "nudgebee", "relay-server")
		assert.Contains(t, b, "at commit `cf1eb58e`")
		assert.Contains(t, b, "establish whether it is already there")
		assert.Contains(t, b, "the revision the running image was built from")
		assert.Contains(t, b, "Compare the code at that revision with the repository's default branch")
		assert.Contains(t, b, "Do not propose a change that another revision already contains")
		assert.Contains(t, b, "not yours to decide",
			"release policy is the operator's, not something the prompt decides")
	})

	t.Run("source without a commit omits the rollout rule", func(t *testing.T) {
		b := buildCodeCapabilityBlock(&services_server.EventCodeCapabilities{
			Source: &services_server.SourceCapability{Repo: "nudgebee/nudgebee-enterprise"},
		}, "nudgebee", "relay-server")
		assert.Contains(t, b, "establish whether it is already there")
		assert.NotContains(t, b, "the revision the running image was built from")
	})

	t.Run("deployment-only capability is unchanged", func(t *testing.T) {
		b := buildCodeCapabilityBlock(&services_server.EventCodeCapabilities{
			Deployment: &services_server.DeploymentCapability{
				Repo:       "nudgebee/nudgebee-infra",
				ValuesPath: "deploy/kubernetes/relay-server/values-dev.yaml",
			},
		}, "nudgebee", "relay-server")
		assert.Contains(t, b, "values-dev.yaml")
		assert.NotContains(t, b, "already there")
	})
}
