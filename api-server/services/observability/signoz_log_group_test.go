package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signozGroupSeries is one log-group series as SigNoz returns it, labelled with
// whatever keys the query grouped by.
func signozGroupSeries(nsKey, podKey string) map[string]any {
	return map[string]any{
		"labels": map[string]any{
			"severity_text": "ERROR",
			nsKey:           "payments",
			podKey:          "checkout-7d4b9c6f8-xk2p9",
		},
		"points": []any{map[string]any{"timestamp": float64(1700000000000), "value": float64(4)}},
	}
}

func signozGroupResult(series map[string]any) map[string]any {
	return map[string]any{"result": []any{map[string]any{"queryName": "A", "series": []any{series}}}}
}

// Current SigNoz labels series with the dotted OTel keys we now group by; a
// legacy response labelled with the underscored keys must parse identically.
func TestSignozLogGroupParse_DottedAndLegacyKeysAgree(t *testing.T) {
	agent := &SignozSource{}
	for name, keys := range map[string][2]string{
		"current (dotted)":     {signozNamespaceKey, signozPodKey},
		"legacy (underscored)": {signozLegacyNamespaceKey, signozLegacyPodKey},
	} {
		t.Run(name, func(t *testing.T) {
			series := signozGroupSeries(keys[0], keys[1])

			saas, err := parseSignozSaasLogGroupResponse(map[string]any{"data": signozGroupResult(series)}, 1700000000000)
			require.NoError(t, err)
			require.Len(t, saas.Groups, 1)

			relayed, err := agent.parseSignozLogGroupResponse(map[string]any{"data": map[string]any{"data": signozGroupResult(series)}}, 1700000000000)
			require.NoError(t, err)
			require.Len(t, relayed.Groups, 1)

			for _, g := range []LogGroup{saas.Groups[0], relayed.Groups[0]} {
				assert.Equal(t, "payments", g.Namespace)
				assert.Equal(t, "ERROR", g.Level)
				assert.NotEmpty(t, g.Workload)
				assert.Equal(t, "/k8s/payments/"+g.Workload, g.ContainerID)
			}
		})
	}
}

// Filtering by the underscored key matches no row on current SigNoz, which
// empties the whole log-group result — both builders must use the dotted keys.
func TestSignozLogGroupFilters_UseDottedKeys(t *testing.T) {
	req := FetchLogGroupRequest{Request: map[string]any{"selectedNamespace": "payments", "selectedWorkload": "checkout"}}

	for name, filters := range map[string][]map[string]any{
		"user source":  buildSignozSaasLogGroupFilters(req),
		"agent source": (&SignozSource{}).buildLogGroupFilters(req),
	} {
		t.Run(name, func(t *testing.T) {
			keys := map[string]any{}
			for _, f := range filters {
				if k, ok := f["key"].(map[string]string); ok {
					keys[k["key"]] = f["value"]
				}
			}
			assert.Equal(t, "payments", keys[signozNamespaceKey])
			assert.Equal(t, "checkout", keys[signozPodKey])
			assert.NotContains(t, keys, signozLegacyNamespaceKey)
			assert.NotContains(t, keys, signozLegacyPodKey)
		})
	}
}
