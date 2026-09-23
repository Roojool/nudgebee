package azure

import (
	"testing"

	"nudgebee/collector/cloud/providers"

	"github.com/stretchr/testify/require"
)

func TestExecuteCliCommandV2RejectsCredentialAndWrongCliCommands(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{command: "az login", want: "not allowed"},
		{command: "az --debug login", want: "not allowed"},
		{command: "az logout", want: "not allowed"},
		{command: "az account clear", want: "not allowed"},
		{command: "az account get-access-token", want: "cannot export"},
		{command: "az aks get-credentials --name cluster", want: "cannot export"},
		{command: "az storage blob download --container-name c --name n --file local", want: "cannot transfer"},
		{command: "gcloud projects list", want: "must start with az"},
	}
	for _, tt := range tests {
		_, err := (&azureProvider{}).ExecuteCliCommandV2(nil, providers.Account{}, tt.command)
		require.ErrorContains(t, err, tt.want)
	}
}
