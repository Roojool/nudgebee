package api

import (
	"encoding/json"
	"strings"
	"testing"

	"nudgebee/llm/common"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func checkBlock(tool, query, expect string) string {
	return "## Root Cause\nsomething\n\n<confirming_check>\ntool: " + tool + "\nquery: " + query + "\nexpect: " + expect + "\n</confirming_check>\n"
}

func TestParseConfirmingCheckBlock(t *testing.T) {
	const ns, wl = "nudgebee", "k8s-collector-worker"
	tests := []struct {
		name        string
		text        string
		wantCheck   bool
		wantReason  string
		podSpecific bool
		fixedTime   bool
	}{
		{name: "absent", text: "## Root Cause\nno block here"},
		{
			// The Sep-9 401 case, written the way the prompt asks.
			name:      "specific key in expect",
			text:      checkBlock("logs_execute_v2", `{app="k8s-collector-worker", namespace="nudgebee"} |= "no k8s agent with access_key" between {window_start} and {window_end}`, "Agent auth failed: no k8s agent with access_key 4323ed4a"),
			wantCheck: true,
		},
		{
			name:      "workload named in expect",
			text:      checkBlock("kubectl_execute", "kubectl describe deployment k8s-collector-worker -n nudgebee", "k8s-collector-worker memory limit 277Mi"),
			wantCheck: true,
		},
		{
			name:       "generic word only",
			text:       checkBlock("logs_execute_v2", `{app="k8s-collector-worker"} between {window_start} and {window_end}`, "error"),
			wantReason: confirmingCheckGenericExpect,
		},
		{
			name:       "status code is not specific",
			text:       checkBlock("logs_execute_v2", `{app="k8s-collector-worker"}`, "401 Unauthorized"),
			wantReason: confirmingCheckGenericExpect,
		},
		{
			name:       "workspace file is never proof",
			text:       checkBlock("shell_execute", "grep -c 'access_key' evidence_logs_5038d801-363b-4ec6-8ce3-95a5aff7cb97.txt", "access_key 4323ed4a"),
			wantReason: confirmingCheckWorkspaceFile,
		},
		{
			name:       "downloaded log file is never proof",
			text:       checkBlock("shell_execute", "grep ERROR logs_loki_1788967432635807497.txt", "access_key 4323ed4a"),
			wantReason: confirmingCheckWorkspaceFile,
		},
		{
			name:       "placeholder left in",
			text:       checkBlock("<the exact name of the tool you called>", "kubectl get pods", "redis-cache used_mem 1046/1048"),
			wantReason: confirmingCheckPlaceholder,
		},
		{
			name:       "missing field",
			text:       "<confirming_check>\ntool: kubectl_execute\nexpect: redis-cache used_mem 1046/1048\n</confirming_check>",
			wantReason: confirmingCheckIncomplete,
		},
		{
			name:        "pod name is flagged, not rejected",
			text:        checkBlock("kubectl_execute", "kubectl logs k8s-collector-worker-777f98b4f4-mb9b2 -n nudgebee", "no k8s agent with access_key 4323ed4a"),
			wantCheck:   true,
			podSpecific: true,
		},
		{
			name:      "fixed time is flagged, not rejected",
			text:      checkBlock("logs_execute_v2", `{"start_time":"2026-09-09 15:15:00 UTC","time_range":"15m"}`, "no k8s agent with access_key 4323ed4a"),
			wantCheck: true,
			fixedTime: true,
		},
		{
			name:      "multi-line query is kept whole",
			text:      "<confirming_check>\ntool: prometheus_execute\nquery: sum by (pod) (\n  rate(x_total{namespace=\"nudgebee\"}[5m])\n)\nexpect: k8s-collector-worker above 0.2\n</confirming_check>",
			wantCheck: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check, reason := parseConfirmingCheckBlock(tt.text, ns, wl)
			assert.Equal(t, tt.wantReason, reason)
			if !tt.wantCheck {
				assert.Nil(t, check)
				return
			}
			require.NotNil(t, check)
			assert.NotEmpty(t, check.Tool)
			assert.NotEmpty(t, check.Query)
			assert.NotEmpty(t, check.Expect)
			assert.Equal(t, tt.podSpecific, check.PodSpecific, "pod_specific")
			assert.Equal(t, tt.fixedTime, check.FixedTime, "fixed_time")
		})
	}
}

func TestIsSpecificExpect(t *testing.T) {
	const ns, wl = "nudgebee", "k8s-collector-worker"
	generic := []string{
		"error", "401 Unauthorized", "high-error-rate observed", "timeout_error seen in logs",
		"crash-loop detected", "connection_refused on port", "rate-limit exceeded", "OOMKilled",
		"CrashLoopBackOff", "image-pull-back-off",
	}
	for _, e := range generic {
		assert.False(t, isSpecificExpect(e, ns, wl), e)
	}
	specific := []string{
		"k8s-collector-worker restarted", "no k8s agent with access_key 4323ed4a", "redis-cache used_mem 1046/1048",
		"POST /gha-webhook returns 404", "Image: nginx:does-not-exist-9.9.9", "payment-gateway service missing",
		"nudgebee namespace quota", "cannot write /data/report.csv: permission denied",
	}
	for _, e := range specific {
		assert.True(t, isSpecificExpect(e, ns, wl), e)
	}
}

func TestParseConfirmingCheckBlock_MultiLineQuery(t *testing.T) {
	text := "<confirming_check>\ntool: prometheus_execute\nquery: sum by (pod) (\n  rate(x_total[5m])\n)\nexpect: k8s-collector-worker above 0.2\n</confirming_check>"
	check, _ := parseConfirmingCheckBlock(text, "nudgebee", "k8s-collector-worker")
	require.NotNil(t, check)
	assert.Equal(t, "sum by (pod) (\n  rate(x_total[5m])\n)", check.Query)
}

func TestStripConfirmingCheckBlock(t *testing.T) {
	text := checkBlock("kubectl_execute", "kubectl get deploy x", "x replicas 0/3")
	stripped := stripConfirmingCheckBlock(text)
	assert.NotContains(t, stripped, "confirming_check")
	assert.Equal(t, "## Root Cause\nsomething", stripped)
	assert.Equal(t, "no block", stripConfirmingCheckBlock("no block"))
}

func TestBuildConfirmingCheckBlock(t *testing.T) {
	block := buildConfirmingCheckBlock("nudgebee", "k8s-collector-worker")
	for _, want := range []string{"<confirming_check>", "tool:", "query:", "expect:", "</confirming_check>", "`nudgebee/k8s-collector-worker`", "{window_start}", "{window_end}"} {
		assert.Contains(t, block, want)
	}
	assert.Contains(t, buildConfirmingCheckBlock("", ""), "the resource")
	// The instruction's own placeholder lines must be rejected if echoed back.
	_, reason := parseConfirmingCheckBlock(block, "nudgebee", "k8s-collector-worker")
	assert.Equal(t, confirmingCheckPlaceholder, reason)
}

func TestMarshalInvestigationAnalysis(t *testing.T) {
	plan := &codeChangePlan{Kind: "deployment", Files: []string{"values.yaml"}, Change: "raise limit", TargetValue: "512Mi"}
	check := &confirmingCheck{Tool: "kubectl_execute", Query: "kubectl describe deploy x", Expect: "x memory limit 277Mi"}

	t.Run("neither keeps the column empty", func(t *testing.T) {
		assert.Equal(t, "", marshalInvestigationAnalysis(nil, nil))
	})

	t.Run("plan only reads back unchanged", func(t *testing.T) {
		got := unmarshalCodeChangePlan(marshalInvestigationAnalysis(plan, nil))
		require.NotNil(t, got)
		assert.Equal(t, *plan, *got)
		assert.NotContains(t, marshalInvestigationAnalysis(plan, nil), "confirming_check")
	})

	t.Run("check only is not mistaken for a plan", func(t *testing.T) {
		raw := marshalInvestigationAnalysis(nil, check)
		assert.Nil(t, unmarshalCodeChangePlan(raw))
		var doc struct {
			ConfirmingCheck *confirmingCheck `json:"confirming_check"`
			Kind            string           `json:"kind"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &doc))
		require.NotNil(t, doc.ConfirmingCheck)
		assert.Equal(t, check.Expect, doc.ConfirmingCheck.Expect)
		assert.Empty(t, doc.Kind)
	})

	t.Run("both keep the plan readable", func(t *testing.T) {
		raw := marshalInvestigationAnalysis(plan, check)
		got := unmarshalCodeChangePlan(raw)
		require.NotNil(t, got)
		assert.Equal(t, *plan, *got)
		assert.True(t, strings.Contains(raw, `"confirming_check"`))
	})
}

func TestToolEvidenceInMessage_Undeterminable(t *testing.T) {
	ran, seen := toolEvidenceInMessage(nil, "3827a6fe-4d43-4423-b470-2acfe26e9c8d", "kubectl_execute", "x")
	assert.Nil(t, ran)
	assert.Nil(t, seen)
	ran, seen = toolEvidenceInMessage(nil, "not-a-uuid", "kubectl_execute", "x")
	assert.Nil(t, ran)
	assert.Nil(t, seen)
}

func TestToolEvidenceInMessage_Query(t *testing.T) {
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()
	dbm := &common.DatabaseManager{Db: sqlx.NewDb(rawDB, "postgres")}
	msg := "3827a6fe-4d43-4423-b470-2acfe26e9c8d"

	mock.ExpectQuery(`FROM llm_conversation_tool_calls`).
		WithArgs(msg, "kubectl_execute", "image: nginx:does-not-exist-9.9.9").
		WillReturnRows(sqlmock.NewRows([]string{"ran", "seen"}).AddRow(2, 1))
	ran, seen := toolEvidenceInMessage(dbm, msg, "kubectl_execute", "Image:         nginx:does-not-exist-9.9.9")
	require.NotNil(t, ran)
	require.NotNil(t, seen)
	assert.True(t, *ran)
	assert.True(t, *seen)

	mock.ExpectQuery(`FROM llm_conversation_tool_calls`).
		WillReturnRows(sqlmock.NewRows([]string{"ran", "seen"}).AddRow(1, 0))
	ran, seen = toolEvidenceInMessage(dbm, msg, "kubectl_execute", "something else")
	assert.True(t, *ran)
	assert.False(t, *seen, "described but not observed")
	require.NoError(t, mock.ExpectationsWereMet())
}
