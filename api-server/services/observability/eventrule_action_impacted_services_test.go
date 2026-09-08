package observability

import (
	"log/slog"
	"net/http"
	"testing"

	"nudgebee/services/eventrule/playbooks"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func impactedServicesCtx(tenant, account, aggKey, subjectType, name, namespace string) playbooks.PlaybookActionContext {
	return playbooks.NewPlaybookActionContext(tenant, account, slog.Default(), playbooks.PlaybookEvent{
		Name:             aggKey,
		AggregationKey:   aggKey,
		SubjectType:      subjectType,
		SubjectName:      name,
		SubjectNamespace: namespace,
	})
}

// Moved here with the action itself (it used to live in
// eventrule/playbooks/action_stage22_canexecute_test.go): the gate is what keeps
// this card off every event that is not a crash loop.
func TestImpactedServicesCanAutoExecute(t *testing.T) {
	cases := []struct {
		name   string
		aggKey string
		want   bool
	}{
		{"fires on crash_loop", "report_crash_loop", true},
		{"skips on OOM", "pod_oom_killer_enricher", false},
	}
	action := &impactedServicesAction{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := impactedServicesCtx("t", "a", tc.aggKey, "pod", "p1", "ns")
			assert.Equal(t, tc.want, action.CanAutoExecute(ctx))
		})
	}

	t.Run("skips when the subject is not a pod", func(t *testing.T) {
		ctx := impactedServicesCtx("t", "a", "report_crash_loop", "node", "n1", "")
		assert.False(t, action.CanAutoExecute(ctx))
	})
}

func TestImpactedServicesExecuteRendersTheCallerTable(t *testing.T) {
	recorder := stubUserPrometheus(t, `cluster="prod"`, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"source_workload_name":"checkout","source_workload_namespace":"shop"},
			 "values":[[1700000000,"0.5"],[1700000060,"1.25"]]},
			{"metric":{"source_workload_name":"web","source_workload_namespace":"shop"},
			 "values":[[1700000000,"2"],[1700000060,"3.5"]]}]}}`))
	})

	resp, err := (&impactedServicesAction{}).Execute(
		impactedServicesCtx("tenant-1", "acc", "report_crash_loop", "pod", "cart-0", "shop"),
		map[string]any{"pod_name": "cart-0", "namespace": "shop"},
	)
	require.NoError(t, err)

	table, ok := resp.(playbooks.PlaybookActionResponseTable)
	require.True(t, ok, "impacted services renders a table card")
	assert.Equal(t, []string{"Service", "Namespace", "Requests/sec (avg 30m)"}, table.Headers)
	require.Len(t, table.Rows, 2)
	// The last sample of each series, formatted as before the migration.
	assert.Equal(t, []any{"checkout", "shop", "1.250"}, table.Rows[0])
	assert.Equal(t, []any{"web", "shop", "3.500"}, table.Rows[1])
	assert.Equal(t, "impacted_services_enricher", table.AdditionalInfo["action_name"])

	// Proves it went through the metrics layer: the account's integration labels are
	// pinned onto the selector, which the relay facade never did for this card.
	query := recorder.all()[0].URL.Query().Get("query")
	assert.Contains(t, query, `cluster="prod"`)
	assert.Contains(t, query, `destination_workload_name="cart-0"`)
}

func TestImpactedServicesExecuteRequiresPodAndTenant(t *testing.T) {
	_, err := (&impactedServicesAction{}).Execute(
		impactedServicesCtx("t", "a", "report_crash_loop", "pod", "p", "ns"),
		map[string]any{"pod_name": "", "namespace": "shop"})
	require.Error(t, err)

	_, err = (&impactedServicesAction{}).Execute(
		impactedServicesCtx("", "", "report_crash_loop", "pod", "p", "ns"),
		map[string]any{"pod_name": "cart-0", "namespace": "shop"})
	require.ErrorContains(t, err, "tenant ID and account ID required")
}
