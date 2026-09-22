package api

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"nudgebee/llm/events"
)

// composeDetailedResponse assembles the canonical RCA report (`detailed_response`)
// from material the pipeline already produced, with no LLM call.
//
// #38925: this stage used to be a second model pass that rewrote the
// investigation into a report. That pass ran no tools — it could not reach the
// cluster or the cloud account — so it could only reformat what Step 2 had
// already said, and it produced a confident-looking report even when the
// investigation had failed. Measured over 511 dev reports (14 days):
//
//   - "Recommended Actions" was already present in 500/506 investigations.
//   - "Code Impact" appeared in 338 reports, but 123 of those were filler
//     ("no code-level bugs identified") and, of the 96 that named a source file,
//     95 named a file the investigation text had already cited.
//   - "Affected Components" (460) and "Historical Pattern" (443) were the only
//     sections the rewrite genuinely added — and both are plain event facts.
//
// So everything the rewrite contributed is either already in the investigation
// or derivable here. Sections follow the order the retired
// `event_detailed_response_synthesis` prompt asked for: Root Cause / Evidence /
// Recommended Actions arrive inside the investigation markdown, then Affected
// Components, Code Impact and Historical Pattern are appended.
//
// Returns "" when there is no investigation to build on; callers must treat that
// as a failed stage rather than writing a report with nothing behind it.
func composeDetailedResponse(investigation, initialSummary, codeFix string, eventData events.Event) string {
	investigation = strings.TrimSpace(investigation)
	if investigation == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString(investigation)

	if section := renderAffectedComponents(eventData, investigation); section != "" {
		b.WriteString("\n\n")
		b.WriteString(section)
	}
	if section := renderCodeImpact(codeFix); section != "" {
		b.WriteString("\n\n")
		b.WriteString(section)
	}
	if section := renderHistoricalPattern(initialSummary); section != "" {
		b.WriteString("\n\n")
		b.WriteString(section)
	}

	return b.String()
}

// detailedResponseFailureReason states why no report was produced, preferring
// the investigation's own failure detail so the UI shows the timeout / agent
// error that actually stopped the run rather than a generic message.
func detailedResponseFailureReason(investigationRow *events.EventAnalysis) string {
	if investigationRow != nil {
		if reason := strings.TrimSpace(investigationRow.StatusReason); reason != "" {
			return reason
		}
	}
	return "investigation produced no findings"
}

// renderAffectedComponents lists the resources the event names, from the event
// row itself. The retired prompt asked the model to restate these from the
// summary prose; they are columns, so read them directly.
//
// The Resource bullet is dropped when the investigation already names it — the
// investigation opens with its own Event Summary block carrying the resource,
// and two facts blocks in one report that both name the same thing is exactly
// the duplication this stage was rewritten to stop producing.
func renderAffectedComponents(eventData events.Event, investigation string) string {
	resource := strings.TrimSpace(eventData.SubjectName)
	if kind := strings.TrimSpace(eventData.SubjectType); kind != "" && resource != "" {
		resource = kind + "/" + resource
	} else if resource == "" {
		resource = kind
	}

	bullets := []string{}
	addBullet := func(label, value string) {
		if v := strings.TrimSpace(value); v != "" {
			bullets = append(bullets, fmt.Sprintf("- **%s:** `%s`", label, v))
		}
	}
	if !namesResource(investigation, eventData.SubjectName) {
		addBullet("Resource", resource)
	}
	addBullet("Namespace", eventData.SubjectNamespace)
	addBullet("Node", eventData.SubjectNode)
	if owner := strings.TrimSpace(eventData.SubjectOwner); owner != "" {
		if kind := strings.TrimSpace(eventData.SubjectOwnerKind); kind != "" {
			owner = kind + "/" + owner
		}
		addBullet("Owner", owner)
	}
	addBullet("Source", eventData.Source)

	if len(bullets) == 0 {
		return ""
	}
	return "### Affected Components\n" + strings.Join(bullets, "\n")
}

// namesResource reports whether the investigation text refers to this resource
// by name, as a whole token rather than as a substring. A plain Contains would
// drop the Resource bullet for a subject called `api` or `db` the moment the
// prose said "application" or "stdb", which is the one case where the reader
// most needs the name spelled out. A boundary is anything outside the DNS-1123
// label charset (letters, digits, `-`, `_`), so `pod/data-postgresql-0` counts
// as naming `data-postgresql-0` while `data-postgresql-0-backup` does not.
func namesResource(investigation, subjectName string) bool {
	name := strings.TrimSpace(subjectName)
	if name == "" {
		return false
	}
	isNameRune := func(r rune) bool {
		return r == '-' || r == '_' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
	}
	for offset := 0; ; {
		idx := strings.Index(investigation[offset:], name)
		if idx < 0 {
			return false
		}
		start := offset + idx
		end := start + len(name)
		beforeOK := start == 0
		if !beforeOK {
			r, _ := utf8.DecodeLastRuneInString(investigation[:start])
			beforeOK = !isNameRune(r)
		}
		afterOK := end == len(investigation)
		if !afterOK {
			r, _ := utf8.DecodeRuneInString(investigation[end:])
			afterOK = !isNameRune(r)
		}
		if beforeOK && afterOK {
			return true
		}
		offset = start + 1
	}
}

// renderCodeImpact surfaces the Step 3 code findings. Unlike the retired prompt,
// this emits the section only when a code stage actually produced something —
// it never writes "no code-level bugs identified" over an event where no code
// analysis ran.
func renderCodeImpact(codeFix string) string {
	codeFix = strings.TrimSpace(codeFix)
	if !events.HasStoredAnalysisContent(codeFix) {
		return ""
	}
	return "### Code Impact\n" + codeFix
}

// renderHistoricalPattern carries the firing history forward from the Step 1
// summary, whose "### Since when" section is a fixed part of its output
// contract (see buildEventSummaryQuery). Omitted when the summary did not
// produce that section.
func renderHistoricalPattern(initialSummary string) string {
	body := extractMarkdownSection(initialSummary, "Since when")
	if body == "" {
		return ""
	}
	return "### Historical Pattern\n" + body
}

// extractMarkdownSection returns the body under the given ATX heading, stopping
// at the next heading of any level. Matching ignores leading `#` markers, bold
// markers and case, so it survives the small formatting drift models apply to
// headings they were told to emit verbatim. Fenced code blocks are skipped on
// both passes: the input is model output, and a shell comment inside a fence
// would otherwise read as a heading and cut the section short.
func extractMarkdownSection(md, heading string) string {
	want := strings.ToLower(heading)
	lines := strings.Split(md, "\n")

	start := -1
	inCodeBlock := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock || !strings.HasPrefix(trimmed, "#") {
			continue
		}
		title := strings.ToLower(strings.Trim(strings.TrimLeft(trimmed, "# "), " *:"))
		if title == want {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return ""
	}

	var body []string
	inCodeBlock = false
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
		} else if !inCodeBlock && strings.HasPrefix(trimmed, "#") {
			break
		}
		body = append(body, line)
	}
	return strings.TrimSpace(strings.Join(body, "\n"))
}
