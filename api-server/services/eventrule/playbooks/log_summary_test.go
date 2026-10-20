package playbooks

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

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

func TestLogLinesSummary_KlogHeader(t *testing.T) {
	d := LogLinesSummary(`W0917 11:04:22.037377 1080759 container.go:1584] reclassified connection pid=846045 fd=12
W0917 11:04:23.100001 1080759 container.go:1584] reclassified connection pid=846046 fd=13
I0917 11:01:55.747561 1080759 registry.go:686] "detected a new container" pid=1245210 cg="/kubepods.slice/x"`, 3)

	require.Len(t, d.Groups, 2)
	assert.Equal(t, LogLineGroup{Text: "reclassified connection pid=846045 fd=12", Count: 2, Level: "warning", Source: "container.go:1584"}, d.Groups[0])
	assert.Equal(t, "detected a new container", d.Groups[1].Text, "klog structured form leads with the quoted message")
	assert.Equal(t, "registry.go:686", d.Groups[1].Source)
}

func TestSummarizeLogEntries_NewestFirstRows(t *testing.T) {
	// Log providers return rows newest first.
	d := SummarizeLogEntries([]LogEntry{
		{Line: "timeout", Level: "error", Timestamp: "2026-09-17T11:01:55.759381437Z"},
		{Line: "timeout", Level: "error", Timestamp: "2026-09-17T11:00:01.4976Z"},
		{Line: "timeout", Level: "error", Timestamp: "2026-09-17T11:00:30Z"},
	}, 3)

	require.Len(t, d.Groups, 1)
	assert.Equal(t, "2026-09-17T11:00:01.4976Z", d.Groups[0].FirstSeen)
	assert.Equal(t, "2026-09-17T11:01:55.759381437Z", d.Groups[0].LastSeen)
}

func TestSeenRange(t *testing.T) {
	t.Run("an unparsed timestamp never replaces a parsed end", func(t *testing.T) {
		var r seenRange
		for _, ts := range []string{"2024-01-01T10:00:00Z", "not-a-time", "2024-01-01T08:00:00Z"} {
			r.add(ts)
		}
		assert.Equal(t, "2024-01-01T08:00:00Z", r.first)
		assert.Equal(t, "2024-01-01T10:00:00Z", r.last)
	})
	t.Run("nanosecond epochs, newest first", func(t *testing.T) {
		var r seenRange
		for _, ts := range []string{"1726531390000000000", "1726531384000000000", "1726531386000000000"} {
			r.add(ts)
		}
		assert.Equal(t, "1726531384000000000", r.first)
		assert.Equal(t, "1726531390000000000", r.last)
	})
	t.Run("nothing parses: arrival order", func(t *testing.T) {
		var r seenRange
		for _, ts := range []string{"Sep17 a", "Sep17 b"} {
			r.add(ts)
		}
		assert.Equal(t, "Sep17 a", r.first)
		assert.Equal(t, "Sep17 b", r.last)
	})
}

func TestParseLogTime(t *testing.T) {
	want := time.Date(2024, 9, 17, 0, 3, 4, 0, time.UTC)
	for _, ts := range []string{"2024-09-17T00:03:04Z", "1726531384", "1726531384000", "1726531384000000", "1726531384000000000"} {
		at, ok := parseLogTime(ts)
		assert.True(t, ok, ts)
		assert.True(t, want.Equal(at), "%s → %s", ts, at)
	}
	at, ok := parseLogTime("1726531384123456789")
	require.True(t, ok)
	assert.Equal(t, 123456789, at.Nanosecond(), "nanosecond epochs keep full precision")
	at, ok = parseLogTime("1726531384.5")
	require.True(t, ok)
	assert.Equal(t, 500000000, at.Nanosecond())
	at, ok = parseLogTime("1726531384123.5")
	require.True(t, ok, "fractional milliseconds")
	assert.Equal(t, 123500000, at.Nanosecond())
	at, ok = parseLogTime("1726531384123456.5")
	require.True(t, ok, "fractional microseconds")
	assert.Equal(t, 2024, at.Year())
	assert.Equal(t, 123456000, at.Nanosecond())
	for _, notATime := range []string{"Sep17", "Inf", "+Inf", "infinity", "NaN", "1e400"} {
		_, ok = parseLogTime(notATime)
		assert.False(t, ok, notATime)
	}
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

// Once the agent reads logs with timestamps on, every line arrives as
// "<RFC3339Nano> <line>". Grouping, level detection and structured-field parsing
// all read the line from its first character, so the prefix has to come off — and
// it is what finally dates the evidence.
func TestLogLinesSummary_KubeletTimestampPrefix(t *testing.T) {
	output := strings.Join([]string{
		`2026-09-23T15:20:41.100000000Z {"level":"info","msg":"payment-worker starting"}`,
		`2026-09-23T15:20:46.200000000Z {"level":"error","msg":"DB_URL is not set"}`,
		`2026-09-23T15:20:46.300000000Z {"level":"error","msg":"DB_URL is not set"}`,
	}, "\n")

	digest := LogLinesSummary(output, 3)

	assert.Equal(t, 3, digest.TotalLines)
	assert.Equal(t, "2026-09-23T15:20:41.100000000Z", digest.FirstSeen)
	assert.Equal(t, "2026-09-23T15:20:46.300000000Z", digest.LastSeen)
	// The prefix must not reach the grouping key, or the two identical errors
	// become two groups and the JSON stops being read as JSON.
	if assert.NotEmpty(t, digest.Groups) {
		assert.Equal(t, "DB_URL is not set", digest.Groups[0].Text)
		assert.Equal(t, 2, digest.Groups[0].Count)
		assert.Equal(t, "error", digest.Groups[0].Level)
	}
}

// Old agents send no prefix and must keep working unchanged — they will outlive
// the fix on every on-prem install.
func TestLogLinesSummary_UntimestampedPayloadUnchanged(t *testing.T) {
	digest := LogLinesSummary("payment-worker starting\nFATAL: DB_URL is not set\n", 3)

	assert.Equal(t, 2, digest.TotalLines)
	assert.Empty(t, digest.FirstSeen)
	assert.Empty(t, digest.LastSeen)
}

// A container that writes its own timestamp in a form the kubelet does not use
// keeps it: "2026-09-17 09:16:54.467258+00:00 [debug] …" is one line of text.
func TestSplitContainerLogTimestamp(t *testing.T) {
	cases := []struct{ name, line, wantTS, wantLine string }{
		{"kubelet prefix", "2026-09-23T15:20:46.200000000Z boom", "2026-09-23T15:20:46.200000000Z", "boom"},
		{"rabbitmq own format", "2026-09-17 09:16:54.467258+00:00 [debug] hi", "", "2026-09-17 09:16:54.467258+00:00 [debug] hi"},
		{"plain line", "Application started", "", "Application started"},
		{"single token", "starting", "", "starting"},
		{"date only", "2026-09-23 boom", "", "2026-09-23 boom"},
		// Rejected by the cheap shape check rather than by time.Parse — same answer.
		{"too short to be rfc3339", "2026-09-23T15:20 boom", "", "2026-09-23T15:20 boom"},
		{"right length, wrong shape", "2026/09/23T15:20:46Z boom", "", "2026/09/23T15:20:46Z boom"},
		{"rfc3339 with offset", "2026-09-23T15:20:46.2+05:30 boom", "2026-09-23T15:20:46.2+05:30", "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, line := splitContainerLogTimestamp(tc.line)
			assert.Equal(t, tc.wantTS, ts)
			assert.Equal(t, tc.wantLine, line)
		})
	}
}
