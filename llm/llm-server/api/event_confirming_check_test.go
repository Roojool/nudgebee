package api

import (
	"encoding/json"
	"strings"
	"testing"

	"nudgebee/llm/common"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
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
	ran, seen, seenInQuery := toolEvidenceInMessage(nil, "3827a6fe-4d43-4423-b470-2acfe26e9c8d", "kubectl_execute", "kubectl get deploy web", "x")
	assert.Nil(t, ran)
	assert.Nil(t, seen)
	assert.Nil(t, seenInQuery)
	ran, seen, seenInQuery = toolEvidenceInMessage(nil, "not-a-uuid", "kubectl_execute", "kubectl get deploy web", "x")
	assert.Nil(t, ran)
	assert.Nil(t, seen)
	assert.Nil(t, seenInQuery)
}

func TestToolEvidenceInMessage_Query(t *testing.T) {
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()
	dbm := &common.DatabaseManager{Db: sqlx.NewDb(rawDB, "postgres")}
	msg := "3827a6fe-4d43-4423-b470-2acfe26e9c8d"
	query := "kubectl get deployment catalog-api -n benchmark-scenarios -o yaml"

	// The call whose command matches the stored query is the one that counts.
	mock.ExpectQuery(`SELECT id::text AS id`).
		WithArgs(msg, "kubectl_execute").
		WillReturnRows(sqlmock.NewRows([]string{"id", "parameters"}).
			AddRow("11111111-1111-1111-1111-111111111111", `{"command":"kubectl get deployment catalog-api -n benchmark-scenarios -o yaml"}`).
			AddRow("22222222-2222-2222-2222-222222222222", `{"command":"kubectl get pods -n benchmark-scenarios"}`))
	mock.ExpectQuery(`COUNT\(\*\) FILTER`).
		WithArgs(msg, "kubectl_execute", "image: nginx:does-not-exist-9.9.9", pq.Array([]string{"11111111-1111-1111-1111-111111111111"})).
		WillReturnRows(sqlmock.NewRows([]string{"seen", "seen_in_query"}).AddRow(2, 1))
	ran, seen, seenInQuery := toolEvidenceInMessage(dbm, msg, "kubectl_execute", query, "Image:         nginx:does-not-exist-9.9.9")
	require.NotNil(t, ran)
	require.NotNil(t, seenInQuery)
	assert.True(t, *ran)
	assert.True(t, *seen)
	assert.True(t, *seenInQuery)

	// Seen somewhere in the tool's output, but not in the stored command's own
	// output: this is what expect_seen_in_run used to credit (#38489 review).
	mock.ExpectQuery(`SELECT id::text AS id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "parameters"}).
			AddRow("11111111-1111-1111-1111-111111111111", `{"command":"kubectl get deployment payment-worker -n benchmark-scenarios -o yaml"}`))
	mock.ExpectQuery(`COUNT\(\*\) FILTER`).
		WillReturnRows(sqlmock.NewRows([]string{"seen", "seen_in_query"}).AddRow(1, 0))
	ran, seen, seenInQuery = toolEvidenceInMessage(dbm, msg, "kubectl_execute", "kubectl logs -n benchmark-scenarios -l app=payment-worker --tail=50", "FATAL: DB_URL is not set")
	assert.True(t, *ran)
	assert.True(t, *seen)
	assert.False(t, *seenInQuery, "the stored command cannot print that line")

	// No successful call of that tool at all.
	mock.ExpectQuery(`SELECT id::text AS id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "parameters"}))
	ran, seen, seenInQuery = toolEvidenceInMessage(dbm, msg, "kubectl_execute", query, "anything")
	assert.False(t, *ran)
	assert.False(t, *seen)
	assert.False(t, *seenInQuery)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueryMatchesCall(t *testing.T) {
	tests := []struct {
		name  string
		query string
		param string
		want  bool
	}{
		{"same command", "kubectl get deployment catalog-api -n benchmark-scenarios -o yaml",
			`{"command":"kubectl get deployment catalog-api -n benchmark-scenarios -o yaml"}`, true},
		{"reordered flags and a shortened pipeline", "kubectl logs elasticsearch-es-default-0 -n elasticsearch --tail 50 | grep watermark",
			`{"command":"kubectl logs -n elasticsearch elasticsearch-es-default-0 --tail 50"}`, true},
		{"window placeholders ignored", "cloud-collector-server logs in namespace nudgebee between {window_start} and {window_end}",
			`{"question":"cloud-collector-server logs in namespace nudgebee"}`, true},
		{"different workload", "kubectl get deployment catalog-api -n benchmark-scenarios -o yaml",
			`{"command":"kubectl get deployment payment-worker -n other-namespace -o yaml"}`, false},
		{"logs line vs deployment spec", "kubectl logs -n benchmark-scenarios -l app=payment-worker --tail=50",
			`{"command":"kubectl get deployment payment-worker -n benchmark-scenarios -o yaml"}`, false},
		{"empty parameters", "kubectl get deploy web", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, queryMatchesCall(tt.query, tt.param))
		})
	}
}

func TestParseConfirmingCheck_VolatileValue(t *testing.T) {
	block := func(expect string) string {
		return "cause found\n<confirming_check>\ntool: shell_execute\nquery: kubectl describe pod -n otel otel-collector\nexpect: " + expect + "\n</confirming_check>"
	}
	volatile := []string{
		"82% /usr/share/elasticsearch/data",
		"The node was low on resource: memory. Threshold quantity: 100Mi, available: 78196Ki",
		"ip-172-31-32-195.ec2.internal 31465296Ki",
		"couldn't start executing the request in 10.000 seconds",
	}
	for _, e := range volatile {
		check, reason := parseConfirmingCheckBlock(block(e), "otel", "otel-collector")
		require.NotNil(t, check, "must be recorded, not rejected: %s", e)
		assert.Empty(t, reason)
		assert.True(t, check.VolatileValue, "expected a measured reading: %s", e)
	}
	// A reading carries the precision of a measurement; a configured threshold is
	// written round. That, not the presence of a number, is the discriminator —
	// across 117 real expects on dev every integer or single-decimal duration was
	// a configured timeout ("default 30s timeout", "timeout: 10s"), while every
	// millisecond figure with real precision was a measured one.
	volatile = append(volatile, "duration: 16165.692 ms statement: SELECT count(*)")
	for _, e := range volatile[len(volatile)-1:] {
		check, _ := parseConfirmingCheckBlock(block(e), "otel", "otel-collector")
		require.NotNil(t, check)
		assert.True(t, check.VolatileValue, "a measured duration: %s", e)
	}

	stable := []string{
		"nginx:does-not-exist-9.9.9",
		"AADSTS7000215: Invalid client secret provided",
		"FATAL: DB_URL is not set",
		"memory limit 512Mi",
		// Configured thresholds, not readings — flagging these would cost us
		// usable checks for nothing.
		"GetAnomaly using default 30s timeout to ml-k8s-server:9999/anomaly",
		"processors: batch: send_batch_size: 1000 timeout: 10s",
		"readiness probe timeout 1.5 seconds on catalog-api",
		"upstream connect timeout 250ms to quote:8080",
	}
	for _, e := range stable {
		check, reason := parseConfirmingCheckBlock(block(e), "otel", "otel-collector")
		require.NotNil(t, check, "rejected a usable expect: %s (%s)", e, reason)
		assert.False(t, check.VolatileValue, "flagged a stable value: %s", e)
	}
}
