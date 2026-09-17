package playbooks

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentPayload encodes log output the way the cluster agent ships a log file.
func agentPayload(t *testing.T, output string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write([]byte(output))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return "b'" + base64.StdEncoding.EncodeToString(buf.Bytes()) + "'"
}

func TestLogLinesSummary_CrashLoggedWithoutErrorKeyword(t *testing.T) {
	// The crash-loop event in #38633: no error-level line, and the card said there was
	// nothing to show while holding the line that explains the crash.
	d := LogLinesSummary("Application started\nIntentional failure for CrashLoopBackOff demo\n", 3)

	assert.Equal(t, []PlaybookActionResponseInsight{
		{Message: "Last log line: Intentional failure for CrashLoopBackOff demo", Severity: "Info"},
	}, d.Insights)
	assert.Equal(t, 2, d.Summary["total_lines"])
}

func TestLogLinesSummary_StructuredErrorsMergeOnMessage(t *testing.T) {
	// The out-of-memory event in #38633: one error, split by Drain into a pattern per
	// metric series, each led by a timestamp and source location.
	var lines []string
	for i := 0; i < 300; i++ {
		lines = append(lines, fmt.Sprintf(`time=2026-09-17T00:03:%02d.297Z level=INFO msg="scrape ok" target=10.0.0.%d`, i%60, i))
	}
	for _, series := range []string{"dns_lookup_duration_seconds", "otel_sdk_exporter_span_inflight", "db_client_connection_count"} {
		for i := 0; i < 4; i++ {
			lines = append(lines, fmt.Sprintf(`time=2026-09-17T00:03:04.297Z level=ERROR source=combined_appender.go:181 msg="Error when appending sample from OTLP" component=web err="out of order sample" series="{__name__=\"%s\", job=\"j%d\"}"`, series, i))
		}
	}
	d := LogLinesSummary(strings.Join(lines, "\n"), 3)

	assert.Equal(t, []PlaybookActionResponseInsight{
		{Message: "Error logged 12 times: Error when appending sample from OTLP: out of order sample", Severity: "High"},
	}, d.Insights)
	assert.Equal(t, 312, d.Summary["total_lines"])
	assert.Equal(t, map[string]int{"info": 300, "error": 12}, d.Summary["level_breakdown"])
	patterns, ok := d.Summary["log_patterns"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, patterns, 2, "summary patterns match the card's groups")
	assert.Equal(t, "Error when appending sample from OTLP: out of order sample", patterns[0]["template"])
	assert.Equal(t, 12, patterns[0]["count"])
	assert.Contains(t, patterns[0]["example"], "series=", "example keeps the raw line")

	require.Len(t, d.Groups, 2)
	assert.Equal(t, LogLineGroup{
		Text:      "Error when appending sample from OTLP: out of order sample",
		Count:     12,
		Level:     "error",
		FirstSeen: "2026-09-17T00:03:04.297Z",
		LastSeen:  "2026-09-17T00:03:04.297Z",
		Source:    "combined_appender.go:181",
	}, d.Groups[0], "error group first, merged across series")
	assert.Equal(t, "scrape ok", d.Groups[1].Text)
	assert.Equal(t, 300, d.Groups[1].Count)
	assert.Equal(t, "info", d.Groups[1].Level)
}

func TestLogLinesSummary_GroupsUnstructuredLinesByShape(t *testing.T) {
	d := LogLinesSummary("connecting to db-1\nconnecting to db-2\nready\nconnecting to db-3", 3)

	require.Len(t, d.Groups, 2)
	assert.Equal(t, 3, d.Groups[0].Count)
	assert.Equal(t, "connecting to db-1", d.Groups[0].Text, "first line of a group is its example")
	assert.Equal(t, "ready", d.Groups[1].Text)
	assert.Equal(t, "unknown", d.Groups[1].Level)
	assert.Equal(t, 4, d.TotalLines)
}

func TestLogLinesSummary_JSONErrorLine(t *testing.T) {
	d := LogLinesSummary(`{"ts":"2026-09-17T00:00:00Z","level":"error","message":"payment failed","error":"card declined"}`, 3)

	assert.Equal(t, []PlaybookActionResponseInsight{
		{Message: "Error logged: payment failed: card declined", Severity: "High"},
	}, d.Insights)
}

func TestLogLinesSummary_DeclaredLevelWinsOverWords(t *testing.T) {
	d := LogLinesSummary(`{"msg":"Error count is zero","level":"info","ts":1726531384.5,"caller":"stats.go:12"}`, 3)

	require.Len(t, d.Groups, 1)
	assert.Equal(t, "info", d.Groups[0].Level)
	assert.Equal(t, "1726531384.5", d.Groups[0].FirstSeen)
	assert.Equal(t, "stats.go:12", d.Groups[0].Source)
	assert.Equal(t, "Info", d.Insights[0].Severity, "no error group, so the highlight is the last line")
}

func TestLogLinesSummary_TruncatesLongLinesOnRuneBoundary(t *testing.T) {
	d := LogLinesSummary(strings.Repeat("é", 250), 3)

	require.Len(t, d.Insights, 1)
	message := strings.TrimPrefix(d.Insights[0].Message, "Last log line: ")
	assert.Equal(t, maxLogPatternInsightLen+1, len([]rune(message)))
	assert.True(t, strings.HasSuffix(message, "…"))
}

func TestLogLinesSummary_BlankOutput(t *testing.T) {
	d := LogLinesSummary("\n  \n", 3)

	assert.Nil(t, d.Summary)
	assert.Empty(t, d.Insights)
	assert.Empty(t, d.Groups)
}

func TestLogLineText(t *testing.T) {
	assert.Equal(t, "plain text line", logLineText("plain text line"))
	assert.Equal(t, "started", logLineText(`level=info msg=started port=8080`))
	assert.Equal(t, `say "hi"`, logLineText(`msg="say \"hi\""`))
	assert.Equal(t, "boom", logLineText(`level=error err=boom`))
	assert.Equal(t, "not json {", logLineText("not json {"))
	assert.Equal(t, "container wrote this", logLineText(`{"log":"container wrote this","stream":"stdout"}`))
	assert.Equal(t, "hello", logLineText(`{pod-name} level=info msg=hello`), "a brace prefix is not JSON")
}

func TestSummarizeAgentLogPayload(t *testing.T) {
	d := summarizeAgentLogPayload(agentPayload(t, "Application started\nIntentional failure for CrashLoopBackOff demo\n"))
	require.NotNil(t, d.Summary)
	assert.Len(t, d.Insights, 1)
	assert.Equal(t, 2, d.TotalLines)

	d = summarizeAgentLogPayload("b'not-base64!'")
	assert.Nil(t, d.Summary, "undecodable payload yields no summary")
	assert.Empty(t, d.Insights)
}
