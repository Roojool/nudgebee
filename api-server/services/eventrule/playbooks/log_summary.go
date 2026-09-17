package playbooks

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

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
	var lines []string
	for _, raw := range strings.Split(output, "\n") {
		if line := strings.TrimSpace(raw); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return LogLinesDigest{}
	}

	digest := LogLinesDigest{TotalLines: len(lines)}
	levelCounts := map[string]int{}

	// Grouped with the same per-line key the Log Groups feature uses, applied to the
	// text a person reads rather than the raw line: a structured line's timestamp and
	// source location would otherwise make every line its own group.
	type group struct {
		LogLineGroup
		example   string
		levelRank int
		lastIndex int
	}
	var groups []*group
	byKey := map[string]*group{}
	for i, line := range lines {
		fields := structuredLogFields(line)
		text := logLineTextFromFields(line, fields)
		key := logparser.NewPattern(text).Hash()
		g, ok := byKey[key]
		if !ok {
			g = &group{LogLineGroup: LogLineGroup{Text: text, Source: firstNonEmpty(fields["source"], fields["caller"])}, example: line}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.Count++
		g.lastIndex = i
		level := lineLevel(line, fields)
		levelCounts[level.String()]++
		if rank := logLevelRank(level); rank > g.levelRank {
			g.levelRank, g.Level = rank, level.String()
		}
		if ts := firstNonEmpty(fields["time"], fields["ts"], fields["timestamp"], fields["@timestamp"]); ts != "" {
			if g.FirstSeen == "" {
				g.FirstSeen = ts
			}
			g.LastSeen = ts
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
			Message:  "Last log line: " + truncateInsight(logLineText(lines[len(lines)-1])),
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
	switch {
	case msg != "" && errText != "":
		return msg + ": " + errText
	case msg != "":
		return msg
	case errText != "":
		return errText
	}
	return line
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
