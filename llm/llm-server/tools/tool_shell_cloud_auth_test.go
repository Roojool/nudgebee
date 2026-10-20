package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectCloudCLI(t *testing.T) {
	tests := []struct {
		command  string
		provider string
		tool     string
	}{
		{command: "aws ec2 describe-instances", provider: "aws", tool: ToolExecuteAwsCliCommand},
		{command: "gcloud compute instances list", provider: "gcp", tool: ToolExecuteGcpCliCommand},
		{command: "gsutil ls", provider: "gcp", tool: ToolExecuteGcpCliCommand},
		{command: "bq query 'select 1'", provider: "gcp", tool: ToolExecuteGcpCliCommand},
		{command: "az vm list", provider: "azure", tool: ToolExecuteAzureCliCommand},
		{command: "kubectl get pods", provider: "k8s", tool: ToolExecuteKubectlCommand},
		{command: "echo harmless", provider: "", tool: ""},
		{command: "echo awsome", provider: "", tool: ""},
	}
	for _, tt := range tests {
		provider, tool := detectCloudCLI(tt.command)
		assert.Equal(t, tt.provider, provider, tt.command)
		assert.Equal(t, tt.tool, tool, tt.command)
	}
}
