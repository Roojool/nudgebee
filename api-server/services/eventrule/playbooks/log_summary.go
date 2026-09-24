package playbooks

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nudgebee/logparser"
)

const logSummaryMaxPatterns = 10

// maxLogPatternInsightLen bounds one highlight line. The card summary is a single
// line; a stack trace or a JSON body as the example would push everything else off it.
const maxLogPatternInsightLen = 200

// ComputeLogSummary runs Drain3 pattern clustering and level detection on log data.
// Returns a map suitable for additional_info containing level_breakdown and log_patterns.
// Expects logs as []map[string]any with "body" and optional "severity_text" keys.
func ComputeLogSummary(logs []map[string]any) map[string]any {
	if len(logs) == 0 {
		return nil
	}

	// Collect message bodies
	var allBodies []string
	for _, logData := range logs {
		body, _ := logData["body"].(string)
		if body != "" {
			allBodies = append(allBodies, body)
		}
	}

	if len(allBodies) == 0 {
		return nil
	}

	// Level breakdown
	levelCounts := map[string]int{}
	for _, logData := range logs {
		// Use severity_text if available
		if severity, ok := logData["severity_text"].(string); ok && severity != "" {
			levelCounts[strings.ToLower(severity)]++
			continue
		}
		// Fall back to logparser level detection
		body, _ := logData["body"].(string)
		if body != "" {
			level := logparser.GuessLevel(body)
			if level != logparser.LevelUnknown {
				levelCounts[level.String()]++
			} else {
				levelCounts["unknown"]++
			}
		}
	}

	// Extract top patterns using Drain3
	patterns := logparser.ExtractPatterns(allBodies, logSummaryMaxPatterns)
	patternList := make([]map[string]any, 0, len(patterns))
	for _, p := range patterns {
		patternList = append(patternList, map[string]any{
			"template":   p.Template,
			"count":      p.Count,
			"percentage": p.Percentage,
			"example":    p.Example,
		})
	}

	summary := map[string]any{
		"total_lines":     len(allBodies),
		"level_breakdown": levelCounts,
		"log_patterns":    patternList,
	}

	return summary
}

// LogLinesSummary groups a container's raw log output and turns it into the card's
// grouped view, its highlights, and a log_summary in the shape ComputeLogSummary writes.
//
// Agent log files arrive as one blob with no severity field, and the only highlight
// they used to get was the browser counting lines that contain an error keyword. A
// crash logged in other words ("Intentional failure for CrashLoopBackOff demo") then
// produced no highlight at all, and the card said there was nothing to show while
// holding the root cause.
//
// Each highlight says what it is, because a bare log line in the summary reads as more
// log output: error-level lines are grouped and reported as "Error logged N times: …";
// with no error-level lines, the one highlight is the last line the container printed,
// which is usually why it stopped.
func LogLinesSummary(output string, maxInsights int) LogLinesDigest {
	var entries []LogEntry
	for _, raw := range strings.Split(output, "\n") {
		ts, line := splitContainerLogTimestamp(raw)
		entries = append(entries, LogEntry{Line: line, Timestamp: ts})
	}
	return SummarizeLogEntries(entries, maxInsights)
}

// splitContainerLogTimestamp peels the RFC3339 timestamp the kubelet prefixes to
// each line when logs are read with timestamps on, returning it separately from
// the line's own text.
//
// The agent reads container logs without that flag today, which is why 58% of
// stored pod-log evidence (dev, week to 2026-09-23) carries no time at all and no
// audit can say which window it covers. Taking the prefix off here rather than
// leaving it on the line matters for more than the timestamp: everything below
// parses the line as JSON or logfmt from its first character, so a prefixed
// `{"msg":…}` would stop being structured and every line would land in its own
// group. Payloads without the prefix are unchanged, so this holds for old and new
// agents alike — which it must, since on-prem agents upgrade on their own
// schedule.
func splitContainerLogTimestamp(line string) (string, string) {
	head, rest, found := strings.Cut(line, " ")
	if !found {
		return "", line
	}
	// Shape first, because this runs on every line of every payload and most
	// lines are not timestamped: an RFC3339 timestamp is at least
	// "2006-01-02T15:04:05Z" and always carries its dashes in the same places, so
	// two byte comparisons reject an ordinary log line ~14x cheaper than letting
	// time.Parse discover it (87.7us -> 6.4us per 1000 unprefixed lines).
	// time.Parse still decides; this only skips the ones that cannot pass it.
	if len(head) < 20 || head[4] != '-' || head[7] != '-' {
		return "", line
	}
	// Only the kubelet's own form: a bare date, or a `2026-09-17 09:16:54+00:00`
	// the container itself wrote, must stay part of the text.
	if _, err := time.Parse(time.RFC3339Nano, head); err != nil {
		return "", line
	}
	return head, rest
}

// LogEntry is one log record to group. Level and Timestamp are what the log source
// declared for it, when it declared anything; a line's own fields are the fallback.
type LogEntry struct {
	Line      string
	Level     string
	Timestamp string
}

// SummarizeLogEntries is LogLinesSummary for logs that arrive as records rather than
// one blob — a log provider's rows, each with its own severity and timestamp.
func SummarizeLogEntries(entries []LogEntry, maxInsights int) LogLinesDigest {
	var lines []LogEntry
	for _, e := range entries {
		if e.Line = strings.TrimSpace(e.Line); e.Line != "" {
			lines = append(lines, e)
		}
	}
	if len(lines) == 0 {
		return LogLinesDigest{}
	}

	digest := LogLinesDigest{TotalLines: len(lines)}
	levelCounts := map[string]int{}
	// The span the payload as a whole covers, so the evidence can say which window
	// it is evidence *of*. Without it a stale card is indistinguishable from a
	// fresh one: agent log evidence is a tail of the container's lifetime, not a
	// window around the event, and 54 of the 239 timestamped pod-log evidences on
	// dev in the week to 2026-09-23 ended more than 5 minutes before their event —
	// one of them 68 days before.
	var overall seenRange

	// Grouped with the same per-line key the Log Groups feature uses, applied to the
	// text a person reads rather than the raw line: a structured line's timestamp and
	// source location would otherwise make every line its own group.
	type group struct {
		LogLineGroup
		seen      seenRange
		example   string
		levelRank int
		lastIndex int
	}
	var groups []*group
	byKey := map[string]*group{}
	for i, entry := range lines {
		line := entry.Line
		body, klogSource := splitKlogHeader(line)
		fields := structuredLogFields(body)
		// klog's structured form leads with the quoted message: `"detected a new container" pid=12`.
		if quoted, err := strconv.QuotedPrefix(body); err == nil && fields["msg"] == "" {
			if msg, err := strconv.Unquote(quoted); err == nil {
				fields["msg"] = msg
			}
		}
		text := logLineTextFromFields(body, fields)
		key := logparser.NewPattern(text).Hash()
		g, ok := byKey[key]
		if !ok {
			g = &group{LogLineGroup: LogLineGroup{Text: text, Source: firstNonEmpty(fields["source"], fields["caller"], klogSource)}, example: line}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.Count++
		g.lastIndex = i
		level := lineLevel(line, fields)
		if declared := logparser.GuessLevel(entry.Level); declared != logparser.LevelUnknown {
			level = declared
		}
		levelCounts[level.String()]++
		if rank := logLevelRank(level); rank > g.levelRank {
			g.levelRank, g.Level = rank, level.String()
		}
		if ts := firstNonEmpty(entry.Timestamp, fields["time"], fields["ts"], fields["timestamp"], fields["@timestamp"]); ts != "" {
			g.seen.add(ts)
			overall.add(ts)
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].levelRank != groups[j].levelRank {
			return groups[i].levelRank > groups[j].levelRank
		}
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		return groups[i].lastIndex > groups[j].lastIndex
	})
	// log_summary is built from the same groups as the card, so the investigation reads
	// the same counts a person sees. A Drain pass here split one error into a pattern per
	// metric series while the card showed it as one group of 898.
	patternList := make([]map[string]any, 0, logSummaryMaxPatterns)
	for _, g := range groups {
		if len(patternList) >= logSummaryMaxPatterns {
			break
		}
		patternList = append(patternList, map[string]any{
			"template":   g.Text,
			"count":      g.Count,
			"percentage": float64(g.Count) * 100 / float64(len(lines)),
			"example":    g.example,
		})
	}
	digest.FirstSeen, digest.LastSeen = overall.first, overall.last
	digest.Summary = map[string]any{
		"total_lines":     len(lines),
		"level_breakdown": levelCounts,
		"log_patterns":    patternList,
	}

	for _, g := range groups {
		if len(digest.Groups) >= maxLogLineGroups {
			break
		}
		if g.Level == "" {
			g.Level = logparser.LevelUnknown.String()
		}
		g.FirstSeen, g.LastSeen = g.seen.first, g.seen.last
		digest.Groups = append(digest.Groups, g.LogLineGroup)
	}

	errorRank := logLevelRank(logparser.LevelError)
	for _, g := range groups {
		if len(digest.Insights) >= maxInsights || g.levelRank < errorRank {
			break
		}
		message := "Error logged: " + truncateInsight(g.Text)
		if g.Count > 1 {
			message = fmt.Sprintf("Error logged %d times: %s", g.Count, truncateInsight(g.Text))
		}
		digest.Insights = append(digest.Insights, PlaybookActionResponseInsight{Message: message, Severity: "High"})
	}
	if len(digest.Insights) == 0 {
		digest.Insights = []PlaybookActionResponseInsight{{
			Message:  "Last log line: " + truncateInsight(logLineText(lines[len(lines)-1].Line)),
			Severity: "Info",
		}}
	}
	return digest
}

// maxLogLineGroups bounds the grouped view stored with the evidence. A log with more
// distinct groups than this is not being summarised by a list anyway.
const maxLogLineGroups = 20

// LogLinesDigest is what LogLinesSummary makes of a container's log output.
type LogLinesDigest struct {
	// Summary is in the shape the provider log actions store as additional_info.log_summary,
	// with patterns taken from Groups.
	Summary map[string]any
	// Groups is the grouped view the log card shows, most severe then most frequent first.
	Groups     []LogLineGroup
	Insights   []PlaybookActionResponseInsight
	TotalLines int
	// FirstSeen and LastSeen are the span the whole payload covers, verbatim from
	// the lines, and empty when no line carried a parseable timestamp.
	FirstSeen string
	LastSeen  string
}

// LogLineGroup is one group of log lines that say the same thing.
type LogLineGroup struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
	Level string `json:"level"`
	// FirstSeen and LastSeen are the line's own timestamps, verbatim, when the line
	// carries one. Container output often does not.
	FirstSeen string `json:"first_seen,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
	Source    string `json:"source,omitempty"`
}

// seenRange is the span a group's lines cover, kept as the timestamps were written.
// Times are compared as times, because log providers return rows newest first. Once one
// timestamp in a group parses, unparsed ones are ignored rather than allowed to replace
// either end; a group with no parseable timestamp keeps arrival order.
type seenRange struct {
	first, last     string
	firstAt, lastAt time.Time
	hasParsed       bool
}

func (r *seenRange) add(ts string) {
	at, ok := parseLogTime(ts)
	switch {
	case ok && !r.hasParsed:
		r.first, r.last, r.firstAt, r.lastAt, r.hasParsed = ts, ts, at, at, true
	case ok:
		if at.Before(r.firstAt) {
			r.first, r.firstAt = ts, at
		}
		if at.After(r.lastAt) {
			r.last, r.lastAt = ts, at
		}
	case !r.hasParsed:
		if r.first == "" {
			r.first = ts
		}
		r.last = ts
	}
}

// parseLogTime reads the timestamp forms log sources use: RFC 3339, or an epoch number
// in seconds, milliseconds, microseconds or nanoseconds (SigNoz and CubeAPM send
// nanoseconds).
func parseLogTime(ts string) (time.Time, bool) {
	if at, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return at, true
	}
	// Integers first: a nanosecond epoch (~1.7e18) is past float64's exact range.
	if epoch, err := strconv.ParseInt(ts, 10, 64); err == nil && epoch > 0 {
		switch {
		case epoch >= 1e17:
			return time.Unix(0, epoch).UTC(), true
		case epoch >= 1e14:
			return time.UnixMicro(epoch).UTC(), true
		case epoch >= 1e11:
			return time.UnixMilli(epoch).UTC(), true
		}
		return time.Unix(epoch, 0).UTC(), true
	}
	// Fractional values, as JSON logs often write them, on the same scales.
	epoch, err := strconv.ParseFloat(ts, 64)
	// ParseFloat accepts "Inf" and "NaN", which would otherwise become year 294247 and 1970.
	if err != nil || math.IsInf(epoch, 0) || math.IsNaN(epoch) || epoch <= 0 {
		return time.Time{}, false
	}
	switch {
	case epoch >= 1e17:
		return time.Unix(0, int64(epoch)).UTC(), true
	case epoch >= 1e14:
		return time.UnixMicro(int64(epoch)).UTC(), true
	case epoch >= 1e11:
		return time.UnixMicro(int64(epoch * 1e3)).UTC(), true
	}
	sec, frac := math.Modf(epoch)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC(), true
}

// klogHeader matches the header Kubernetes components write before every line:
// level letter, date, time, thread id and file:line — "I0917 11:01:55.747443 1080759 container.go:202] …".
var klogHeader = regexp.MustCompile(`^[IWEF]\d{4} \d{2}:\d{2}:\d{2}\.\d+\s+\d+ ([^\]\s]+)\] ?`)

// splitKlogHeader returns a klog line's message and its file:line, or the line unchanged.
// The header's timestamp would otherwise lead every group's text, and the level letter is
// already read by logparser.GuessLevel on the full line.
func splitKlogHeader(line string) (string, string) {
	m := klogHeader.FindStringSubmatchIndex(line)
	if m == nil {
		return line, ""
	}
	return line[m[1]:], line[m[2]:m[3]]
}

func logLevelRank(level logparser.Level) int {
	switch level {
	case logparser.LevelCritical:
		return 4
	case logparser.LevelError:
		return 3
	case logparser.LevelWarning:
		return 2
	case logparser.LevelInfo, logparser.LevelDebug:
		return 1
	}
	return 0
}

func truncateInsight(s string) string {
	if runes := []rune(s); len(runes) > maxLogPatternInsightLen {
		return string(runes[:maxLogPatternInsightLen]) + "…"
	}
	return s
}

// logLineText returns what a person needs from a log line: for structured lines (JSON
// or key=value) the message and error fields, otherwise the line itself. A structured
// line leads with a timestamp and source location, and cut to one summary line those
// push the actual error out of view.
func logLineText(line string) string {
	return logLineTextFromFields(line, structuredLogFields(line))
}

// structuredLogFields returns the string and number fields of a JSON or key=value line, keyed in
// lower case. Free text yields an empty map.
func structuredLogFields(line string) map[string]string {
	if strings.HasPrefix(line, "{") {
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) == nil {
			fields := map[string]string{}
			for k, v := range obj {
				switch value := v.(type) {
				case string:
					fields[strings.ToLower(k)] = value
				case float64:
					// Epoch timestamps and numeric levels are common in JSON logs.
					fields[strings.ToLower(k)] = strconv.FormatFloat(value, 'f', -1, 64)
				}
			}
			return fields
		}
		// A line can open with a brace and not be JSON — "{pod-name} level=info msg=…".
	}
	return parseLogfmt(line)
}

func logLineTextFromFields(line string, fields map[string]string) string {
	msg := firstNonEmpty(fields["msg"], fields["message"], fields["log"], fields["body"])
	errText := firstNonEmpty(fields["err"], fields["error"])
	text := line
	switch {
	case msg != "" && errText != "":
		text = msg + ": " + errText
	case msg != "":
		text = msg
	case errText != "":
		text = errText
	}
	// A multi-line record (a stack trace from a log provider) is represented by its
	// first line, where the exception message sits — both for display and for grouping,
	// so the same exception thrown from different call paths is one group.
	if first, _, multi := strings.Cut(text, "\n"); multi {
		text = strings.TrimSpace(first)
	}
	return text
}

// lineLevel prefers the level a structured line declares. Guessing from the whole line
// reads its first words, so {"msg":"Error count is zero","level":"info"} would count as
// an error.
func lineLevel(line string, fields map[string]string) logparser.Level {
	if declared := firstNonEmpty(fields["level"], fields["severity"], fields["lvl"]); declared != "" {
		if level := logparser.GuessLevel(declared); level != logparser.LevelUnknown {
			return level
		}
	}
	return logparser.GuessLevel(line)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseLogfmt reads key=value and key="quoted value" pairs. Anything that is not a
// pair is skipped, so free text yields an empty map.
func parseLogfmt(line string) map[string]string {
	fields := map[string]string{}
	i := 0
	for i < len(line) {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		keyStart := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' {
			i++
		}
		if i >= len(line) || line[i] != '=' {
			continue
		}
		key := strings.ToLower(line[keyStart:i])
		i++
		var value string
		if i < len(line) && line[i] == '"' {
			i++
			var b strings.Builder
			for i < len(line) && line[i] != '"' {
				if line[i] == '\\' && i+1 < len(line) {
					i++
				}
				b.WriteByte(line[i])
				i++
			}
			i++
			value = b.String()
		} else {
			valueStart := i
			for i < len(line) && line[i] != ' ' {
				i++
			}
			value = line[valueStart:i]
		}
		if key != "" {
			fields[key] = value
		}
	}
	return fields
}
