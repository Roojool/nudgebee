package eventrule

import (
	"database/sql"
	"encoding/json"
	"testing"

	"nudgebee/services/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const otelExpr = `sum by (service_name) (rate(rpc_client_duration_count{namespace="demo", rpc_grpc_status_code!="0"}[2m]))
  /
sum by (service_name) (rate(rpc_client_duration_count{namespace="demo"}[2m])) > 0.05`

func otelDefinition(t *testing.T) RuleDefinition {
	t.Helper()
	norm, ok := NormalizePromQL(otelExpr)
	require.True(t, ok)
	return RuleDefinition{Expr: otelExpr, Duration: "1m", ExprNormalized: norm}
}

func TestNormalizePromQL(t *testing.T) {
	a, ok := NormalizePromQL(`rate(x[5m]) > 1`)
	require.True(t, ok)
	// The same rule re-serialised by a sync pass compares equal.
	b, ok := NormalizePromQL("rate( x [5m] )\n  > 1")
	require.True(t, ok)
	assert.Equal(t, a, b)

	// MetricsQL (VictoriaMetrics) is not PromQL: no edit is offered for it.
	_, ok = NormalizePromQL(`rate(x) keep_metric_names`)
	assert.False(t, ok)
	_, ok = NormalizePromQL("")
	assert.False(t, ok)
}

func TestPlanRuleChange(t *testing.T) {
	cur := otelDefinition(t)
	newExpr := `sum by (service_name) (rate(rpc_client_duration_count{namespace="demo", rpc_grpc_status_code!="0", rpc_method!="EventStream"}[2m]))
  /
sum by (service_name) (rate(rpc_client_duration_count{namespace="demo", rpc_method!="EventStream"}[2m])) > 0.05`

	expr, dur, err := planRuleChange(cur, ApplyRuleChangeRequest{Expr: newExpr, ExpectedExpr: otelExpr, ExpectedDuration: "1m"})
	require.NoError(t, err)
	assert.Equal(t, newExpr, expr)
	assert.Equal(t, "1m", dur, "for is kept when not proposed")

	// A whitespace-only difference in the stored expression is not a conflict.
	_, _, err = planRuleChange(cur, ApplyRuleChangeRequest{Expr: newExpr, ExpectedExpr: "  " + otelExpr + "\n", ExpectedDuration: "1m"})
	assert.NoError(t, err)

	// Only `for` changes.
	expr, dur, err = planRuleChange(cur, ApplyRuleChangeRequest{Duration: "10m", ExpectedExpr: otelExpr, ExpectedDuration: "1m"})
	require.NoError(t, err)
	assert.Equal(t, otelExpr, expr)
	assert.Equal(t, "10m", dur)
}

func TestPlanRuleChangeRefusals(t *testing.T) {
	cur := otelDefinition(t)
	cases := []struct {
		name string
		req  ApplyRuleChangeRequest
		want error
	}{
		{"rule edited since the plan", ApplyRuleChangeRequest{Expr: "up == 0", ExpectedExpr: "up == 1", ExpectedDuration: "1m"}, ErrRuleChanged},
		{"for changed since the plan", ApplyRuleChangeRequest{Expr: "up == 0", ExpectedExpr: otelExpr, ExpectedDuration: "5m"}, ErrRuleChanged},
		{"invalid PromQL", ApplyRuleChangeRequest{Expr: "sum(rate(x[5m]", ExpectedExpr: otelExpr, ExpectedDuration: "1m"}, ErrOperationUnavailable},
		{"invalid duration", ApplyRuleChangeRequest{Duration: "ten minutes", ExpectedExpr: otelExpr, ExpectedDuration: "1m"}, ErrOperationUnavailable},
		{"no change", ApplyRuleChangeRequest{Expr: otelExpr + " ", ExpectedExpr: otelExpr, ExpectedDuration: "1m"}, ErrOperationUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := planRuleChange(cur, tc.req)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestEditableDefinition(t *testing.T) {
	editable := &RuleTarget{Rule: toggledRule{Expr: "up == 0", Duration: "5m"}}
	editable.Operations.EditDefinition = Capability{Available: true}
	def := editable.EditableDefinition()
	require.NotNil(t, def)
	assert.Equal(t, "up == 0", def.ExprNormalized)

	notEditable := &RuleTarget{Rule: toggledRule{Expr: "up == 0"}}
	assert.Nil(t, notEditable.EditableDefinition(), "edit not available")

	metricsQL := &RuleTarget{Rule: toggledRule{Expr: "rate(x) keep_metric_names"}}
	metricsQL.Operations.EditDefinition = Capability{Available: true}
	assert.Nil(t, metricsQL.EditableDefinition(), "not PromQL")
}

// A partial update omits the playbook keys and must keep the stored playbook; the
// edit form sends empty arrays to clear it. The two must stay distinguishable
// after the request is decoded the way the RPC handler decodes it.
func TestPlaybookSent(t *testing.T) {
	decode := func(body string) EventConfig {
		t.Helper()
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &m))
		var cfg EventConfig
		require.NoError(t, common.UnmarshalMapToStruct(m, &cfg))
		return cfg
	}
	assert.False(t, playbookSent(decode(`{"alert":"A","expr":"up == 0"}`)), "keys absent: keep the stored playbook")
	assert.True(t, playbookSent(decode(`{"alert":"A","trigger_params":[],"action_params":[]}`)), "empty arrays: clear it")
	assert.True(t, playbookSent(decode(`{"alert":"A","trigger_params":[{"k":"v"}],"action_params":[]}`)))
	assert.False(t, playbookSent(EventConfig{Alert: "A"}), "an EventConfig built in Go (threshold apply, rule fix)")
}

// Stored metadata that cannot be read is an error, never an empty map that would be
// written back over the rule's description and summary.
func TestDecodeStringMap(t *testing.T) {
	m, err := decodeStringMap(sql.NullString{String: `{"description":"d","summary":"s"}`, Valid: true})
	require.NoError(t, err)
	assert.Equal(t, "d", m["description"])

	for _, empty := range []sql.NullString{{}, {String: "", Valid: true}, {String: "null", Valid: true}} {
		m, err := decodeStringMap(empty)
		require.NoError(t, err)
		assert.Empty(t, m["description"], "reading a nil map is safe")
	}

	_, err = decodeStringMap(sql.NullString{String: `{"description":{"nested":true}}`, Valid: true})
	assert.Error(t, err)
}
