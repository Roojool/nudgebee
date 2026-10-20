package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScrubCredentials(t *testing.T) {
	env := map[string]string{
		"GITHUB_TOKEN":          "github-secret-value",
		"GITLAB_TOKEN":          "gitlab-secret-value",
		"AWS_SECRET_ACCESS_KEY": "legacy-aws-secret",
		"AZURE_CLIENT_SECRET":   "legacy-azure-secret",
		"GCP_SA_KEY":            "legacy-gcp-secret",
	}
	output := "github-secret-value gitlab-secret-value legacy-aws-secret legacy-azure-secret legacy-gcp-secret"
	assert.Equal(t, "[REDACTED] [REDACTED] [REDACTED] [REDACTED] [REDACTED]", ScrubCredentials(output, env))
}

func TestScrubCredentialsSkipsShortValues(t *testing.T) {
	assert.Equal(t, "short", ScrubCredentials("short", map[string]string{"GITHUB_TOKEN": "short"}))
}
