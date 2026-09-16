package api

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"nudgebee/llm/common"
	"nudgebee/llm/security"

	"github.com/google/uuid"
)

// confirmingCheck is the one check an investigation names as proof of its root
// cause, in a <confirming_check> block. Phase 1 of #38489's follow-up only
// records it: nothing reads it back yet. The flags below exist so we can measure,
// on real investigations, whether stored checks are specific and re-runnable
// enough to build a "verify the previous cause first" step on.
type confirmingCheck struct {
	Tool   string `json:"tool"`
	Query  string `json:"query"`
	Expect string `json:"expect"`

	// ToolRanInRun / ExpectSeenInRun are nil when they could not be determined
	// (e.g. the investigation was recovered from conversation history).
	// ExpectSeenInRun: the expected text appeared in a successful result of
	// that tool in this run — the check was observed, not just described.
	ToolRanInRun    *bool `json:"tool_ran_in_run,omitempty"`
	ExpectSeenInRun *bool `json:"expect_seen_in_run,omitempty"`
	// PodSpecific / FixedTime mark a check that cannot be re-run for a later
	// firing as written.
	PodSpecific bool      `json:"pod_specific,omitempty"`
	FixedTime   bool      `json:"fixed_time,omitempty"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// Rejection reasons. Bounded set: each is also a metric status value.
const (
	confirmingCheckIncomplete    = "incomplete"
	confirmingCheckPlaceholder   = "placeholder"
	confirmingCheckGenericExpect = "generic_expect"
	confirmingCheckWorkspaceFile = "workspace_file"
)

var (
	confirmingCheckBlockPattern = regexp.MustCompile(`(?is)<confirming_check>(.*?)</confirming_check>`)
	confirmingCheckKeyPattern   = regexp.MustCompile(`(?i)^[ \t]*(tool|query|expect)[ \t]*:[ \t]*(.*)$`)

	// A token specific enough to identify the thing involved: carries a digit or
	// a separator used in names, keys, paths and hosts.
	specificTokenPattern = regexp.MustCompile(`[A-Za-z0-9]*[0-9\-_./:][A-Za-z0-9\-_./:]*`)

	// Files the pipeline or the agent wrote into its own workspace. They belong
	// to one firing and are deleted with it, so they are never proof.
	workspaceFilePattern = regexp.MustCompile(`(?i)\b(evidence_logs_[\w-]+\.txt|event_[\w-]+_evidence\.json|logs_(loki|kubectl|[a-z]+)_\d+\.txt)\b`)

	fixedTimePattern = regexp.MustCompile(`\b20\d{2}-\d{2}-\d{2}[T ]\d{2}:\d{2}|\b1[6-9]\d{11}\b`)
	// A pod name: workload plus a ReplicaSet hash and pod suffix, or a
	// DaemonSet/StatefulSet-style suffix.
	genericPodPattern = regexp.MustCompile(`\b[a-z0-9]([a-z0-9-]*[a-z0-9])?-[a-z0-9]{8,10}-[a-z0-9]{5}\b`)
)

// genericExpectWords are words that appear whenever the alert fires, so they
// confirm the symptom, not the mechanism.
var genericExpectWords = map[string]bool{
	"error": true, "errors": true, "exception": true, "exceptions": true,
	"fail": true, "failed": true, "failure": true, "failures": true,
	"timeout": true, "timeouts": true, "timed": true, "out": true,
	"warning": true, "warn": true, "denied": true, "refused": true,
	"unauthorized": true, "forbidden": true, "oom": true, "oomkilled": true,
	"crash": true, "crashloopbackoff": true, "restart": true, "restarts": true,
	"high": true, "rate": true, "spike": true, "the": true, "a": true, "an": true,
	"is": true, "in": true, "of": true, "and": true, "or": true, "with": true,
	"connection": true, "limit": true, "exceeded": true, "loop": true, "back": true,
	"off": true, "backoff": true, "detected": true, "observed": true, "seen": true,
	"log": true, "logs": true, "on": true, "port": true, "request": true,
	"requests": true, "response": true, "status": true, "code": true, "not": true,
	"found": true, "invalid": true, "bad": true, "down": true, "unavailable": true,
	"image": true, "pull": true, "pod": true, "pods": true, "container": true,
	"containers": true, "memory": true, "cpu": true, "usage": true, "service": true,
	"killed": true, "evicted": true, "pending": true, "ready": true, "unhealthy": true,
}

// specificValueChars mark a token as a value, version, path, host or address.
const specificValueChars = "0123456789./:"

// buildConfirmingCheckBlock is the instruction appended to the investigation
// prompt. The block is optional: an investigation that did not confirm its cause
// with a tool result must omit it.
func buildConfirmingCheckBlock(namespace, workload string) string {
	target := "the resource"
	if workload != "" {
		target = "`" + workload + "`"
		if namespace != "" {
			target = "`" + namespace + "/" + workload + "`"
		}
	}
	var b strings.Builder
	b.WriteString("\n\n## Confirming Check\n")
	b.WriteString("If a tool result you obtained in THIS investigation confirmed the root cause, also end your response with the block below. Omit it when the cause was not confirmed by a tool result you ran.\n")
	b.WriteString("The check must prove the mechanism — the step in your causality chain that explains why — not merely that the alert fired. It must be re-runnable for a later firing: name " + target + " rather than a pod, and write {window_start} / {window_end} instead of fixed times. Never point it at a file in your workspace.\n")
	b.WriteString("<confirming_check>\n")
	b.WriteString("tool: <the exact name of the tool you called>\n")
	b.WriteString("query: <the exact query or command>\n")
	b.WriteString("expect: <the specific text or value in that result that confirms the cause; it must name the specific thing involved — a workload, key, index, path or host — not just a word like error or 401>\n")
	b.WriteString("</confirming_check>\n")
	return b.String()
}

// parseConfirmingCheckBlock extracts and validates the block. It returns
// (nil, "") when the block is absent, and (nil, reason) when it is present but
// rejected.
func parseConfirmingCheckBlock(text, namespace, workload string) (*confirmingCheck, string) {
	m := confirmingCheckBlockPattern.FindStringSubmatch(text)
	if m == nil {
		return nil, ""
	}
	fields := map[string]string{}
	currentKey := ""
	for line := range strings.SplitSeq(m[1], "\n") {
		if kv := confirmingCheckKeyPattern.FindStringSubmatch(line); kv != nil {
			currentKey = strings.ToLower(kv[1])
			fields[currentKey] = kv[2]
		} else if currentKey != "" {
			fields[currentKey] += "\n" + line
		}
	}
	check := &confirmingCheck{
		Tool:   strings.TrimSpace(fields["tool"]),
		Query:  strings.TrimSpace(fields["query"]),
		Expect: strings.TrimSpace(fields["expect"]),
	}
	if check.Tool == "" || check.Query == "" || check.Expect == "" {
		return nil, confirmingCheckIncomplete
	}
	for _, v := range []string{check.Tool, check.Query, check.Expect} {
		if strings.HasPrefix(v, "<") {
			return nil, confirmingCheckPlaceholder
		}
	}
	if workspaceFilePattern.MatchString(check.Query) {
		return nil, confirmingCheckWorkspaceFile
	}
	if !isSpecificExpect(check.Expect, namespace, workload) {
		return nil, confirmingCheckGenericExpect
	}
	check.FixedTime = fixedTimePattern.MatchString(check.Query)
	check.PodSpecific = isPodSpecific(check.Query, workload)
	return check, ""
}

// isSpecificExpect reports whether expect names the specific thing involved: the
// workload or namespace, or a token shaped like a name, key, path or value.
func isSpecificExpect(expect, namespace, workload string) bool {
	lower := strings.ToLower(expect)
	for _, name := range []string{workload, namespace} {
		if len(name) >= 4 && strings.Contains(lower, strings.ToLower(name)) {
			return true
		}
	}
	for _, tok := range specificTokenPattern.FindAllString(expect, -1) {
		tok = strings.Trim(tok, "-_./:")
		if len(tok) < 4 {
			continue
		}
		if strings.ContainsAny(tok, specificValueChars) {
			return true
		}
		// A compound like high-error-rate or timeout_error is still generic;
		// it is specific only if one of its parts is not.
		for _, part := range strings.FieldsFunc(strings.ToLower(tok), func(r rune) bool { return r == '-' || r == '_' }) {
			if len(part) >= 3 && !genericExpectWords[part] {
				return true
			}
		}
	}
	return false
}

func isPodSpecific(query, workload string) bool {
	if workload != "" {
		p := regexp.MustCompile(`\b` + regexp.QuoteMeta(strings.ToLower(workload)) + `-([a-z0-9]{8,10}-)?[a-z0-9]{5}\b`)
		if p.MatchString(strings.ToLower(query)) {
			return true
		}
	}
	return genericPodPattern.MatchString(query)
}

// stripConfirmingCheckBlock removes the block before the investigation text is
// stored, synthesised or shown. No-op when absent.
func stripConfirmingCheckBlock(s string) string {
	return strings.TrimRight(confirmingCheckBlockPattern.ReplaceAllString(s, ""), " \t\r\n")
}

// toolEvidenceInMessage reports, within this investigation's own message,
// whether the named tool completed successfully and whether the expected text
// appeared in one of those results (whitespace-insensitive). Both nil when it
// cannot be determined.
func toolEvidenceInMessage(dbManager *common.DatabaseManager, messageId, tool, expect string) (ran, seen *bool) {
	if dbManager == nil || tool == "" {
		return nil, nil
	}
	if _, err := uuid.Parse(messageId); err != nil {
		return nil, nil
	}
	var counts struct {
		Ran  int `db:"ran"`
		Seen int `db:"seen"`
	}
	if err := dbManager.Db.Get(&counts, `
		SELECT COUNT(*) AS ran,
		       COUNT(*) FILTER (WHERE position($3 in regexp_replace(lower(COALESCE(response, '')), '\s+', ' ', 'g')) > 0) AS seen
		FROM llm_conversation_tool_calls
		WHERE message_id = $1 AND tool_name = $2 AND status = 'success'`,
		messageId, tool, normalizeExpect(expect)); err != nil {
		return nil, nil
	}
	r, s := counts.Ran > 0, counts.Seen > 0
	return &r, &s
}

func normalizeExpect(expect string) string {
	return strings.Join(strings.Fields(strings.ToLower(expect)), " ")
}

// recordConfirmingCheck parses the block from the investigation text, fills the
// measurement flags, and emits the Phase 1 metric and log line. Returns the
// check to store, or nil.
func recordConfirmingCheck(ctx *security.RequestContext, dbManager *common.DatabaseManager, text, messageId, eventId, accountId, namespace, workload string) *confirmingCheck {
	check, reason := parseConfirmingCheckBlock(text, namespace, workload)
	switch {
	case check == nil && reason == "":
		common.MetricsEventAnalysisOperationsTotal("confirming_check", "missing", accountId)
		ctx.GetLogger().Info("analyzer: no confirming check in investigation", "event_id", eventId)
		return nil
	case check == nil:
		common.MetricsEventAnalysisOperationsTotal("confirming_check", "rejected_"+reason, accountId)
		ctx.GetLogger().Info("analyzer: confirming check rejected", "event_id", eventId, "reason", reason)
		return nil
	}
	check.ToolRanInRun, check.ExpectSeenInRun = toolEvidenceInMessage(dbManager, messageId, check.Tool, check.Expect)
	check.RecordedAt = time.Now().UTC()
	common.MetricsEventAnalysisOperationsTotal("confirming_check", "produced", accountId)
	ctx.GetLogger().Info("analyzer: confirming check recorded",
		"event_id", eventId, "tool", check.Tool,
		"tool_ran_in_run", boolOrUnknown(check.ToolRanInRun),
		"expect_seen_in_run", boolOrUnknown(check.ExpectSeenInRun),
		"pod_specific", check.PodSpecific, "fixed_time", check.FixedTime,
		"expect_bytes", len(check.Expect))
	return check
}

func boolOrUnknown(b *bool) string {
	if b == nil {
		return "unknown"
	}
	return fmt.Sprint(*b)
}

// investigationAnalysisDoc is what the investigation row's analysis column
// stores: the change plan's fields at the top level (unchanged, so
// unmarshalCodeChangePlan keeps reading them) plus the confirming check.
type investigationAnalysisDoc struct {
	*codeChangePlan
	ConfirmingCheck *confirmingCheck `json:"confirming_check,omitempty"`
}

// marshalInvestigationAnalysis stores both. Empty when neither is present, so a
// row without either keeps its existing empty value.
func marshalInvestigationAnalysis(plan *codeChangePlan, check *confirmingCheck) string {
	if plan == nil && check == nil {
		return ""
	}
	b, err := json.Marshal(investigationAnalysisDoc{codeChangePlan: plan, ConfirmingCheck: check})
	if err != nil {
		return marshalCodeChangePlan(plan)
	}
	return string(b)
}
