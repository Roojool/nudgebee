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
	"github.com/lib/pq"
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
	// ExpectSeenInQuery is the honest version of ExpectSeenInRun: the expected
	// text appeared in the output of a call whose command matches the query
	// stored above, not merely somewhere in that tool's output during the run.
	// ExpectSeenInRun over-credited — a check storing `kubectl get deployment
	// -o yaml` while expecting a `FATAL:` log line was flagged seen, though that
	// command cannot print a log line. Both are kept so the week-one baseline
	// stays comparable; phase 2 must gate on this one.
	ExpectSeenInQuery *bool `json:"expect_seen_in_query,omitempty"`
	// PodSpecific / FixedTime mark a check that cannot be re-run for a later
	// firing as written.
	PodSpecific bool `json:"pod_specific,omitempty"`
	FixedTime   bool `json:"fixed_time,omitempty"`
	// VolatileValue marks an expect that pins a measured reading — a percentage,
	// a raw byte quantity, a fractional duration, a node address. Those change
	// between firings, so the check cannot re-prove the cause even when the
	// cause is unchanged (dev: Elasticsearch disk expected "82%", the next
	// firing read 87%). Measured, not rejected: the same shapes also carry
	// configured thresholds that are perfectly stable ("high disk watermark
	// [90%] exceeded"), and phase 1 exists to size the problem.
	VolatileValue bool      `json:"volatile_value,omitempty"`
	RecordedAt    time.Time `json:"recorded_at"`
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

	// A measured reading: a percentage, a four-digit-or-longer byte quantity
	// (configured limits are short — 512Mi, 2Gi), a fractional duration, or a
	// node/IP address.
	volatileValuePattern = regexp.MustCompile(`[0-9]+(\.[0-9]+)?\s?%|[0-9]{4,}\s?(Ki|Mi|Gi|KiB|MiB|GiB)\b|[0-9]+\.[0-9]{2,}\s?(seconds|secs|ms|s)\b|\bip-[0-9]+-[0-9]+-[0-9]+-[0-9]+|\b([0-9]{1,3}\.){3}[0-9]{1,3}\b`)

	// Tokens of a command: names, paths, flags, keys and values.
	commandTokenPattern = regexp.MustCompile(`[A-Za-z0-9_{}][A-Za-z0-9\-_./:={}]*`)

	fixedTimePattern = regexp.MustCompile(`\b20\d{2}-\d{2}-\d{2}[T ]\d{2}:\d{2}|\b1[6-9]\d{11}\b`)
	// A pod name: workload plus a ReplicaSet hash and pod suffix, or a
	// DaemonSet/StatefulSet-style suffix.
	genericPodPattern = regexp.MustCompile(`\b[a-z0-9]([a-z0-9-]*[a-z0-9])?-[a-z0-9]{8,10}-[a-z0-9]{5}\b`)
)

// commandFillerWords appear in most commands of a kind, so they say nothing
// about which command a stored query refers to.
var commandFillerWords = map[string]bool{
	"kubectl": true, "get": true, "logs": true, "describe": true, "exec": true,
	"the": true, "for": true, "and": true, "show": true, "why": true, "what": true,
	"true": true, "false": true, "null": true, "sort": true, "grep": true,
}

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
	b.WriteString("expect: <a short fragment of that command's own output that confirms the cause — a few words, not a pasted line. It must name the specific thing involved (a workload, key, index, path, host or image tag), not just a word like error or 401, and it must be text that will read the same the next time this alert fires: no measured value that moves between firings (a percentage, a byte count, a duration, a node address). A configured threshold or a fixed identifier is fine.>\n")
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
	check.VolatileValue = volatileValuePattern.MatchString(check.Expect)
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

// toolEvidenceInMessage reports, within this investigation's own message:
// whether the named tool completed successfully (ran), whether the expected
// text appeared in any of those results (seen — the week-one definition, kept
// for comparability), and whether it appeared in the output of a call whose
// command matches the stored query (seenInQuery — the one phase 2 can trust).
// All nil when it cannot be determined.
func toolEvidenceInMessage(dbManager *common.DatabaseManager, messageId, tool, query, expect string) (ran, seen, seenInQuery *bool) {
	if dbManager == nil || tool == "" {
		return nil, nil, nil
	}
	if _, err := uuid.Parse(messageId); err != nil {
		return nil, nil, nil
	}
	// Parameters are bounded here because a shell_execute heredoc can be large;
	// the command being matched sits at the front. Responses stay in the
	// database — a single tool result can be megabytes.
	var calls []struct {
		Id         string `db:"id"`
		Parameters string `db:"parameters"`
	}
	if err := dbManager.Db.Select(&calls, `
		SELECT id::text AS id, COALESCE(LEFT(parameters, 4000), '') AS parameters
		FROM llm_conversation_tool_calls
		WHERE message_id = $1 AND tool_name = $2 AND status = 'success'`, messageId, tool); err != nil {
		return nil, nil, nil
	}
	r := len(calls) > 0
	if !r {
		f := false
		return &r, &f, &f
	}
	matched := make([]string, 0, len(calls))
	for _, c := range calls {
		if queryMatchesCall(query, c.Parameters) {
			matched = append(matched, c.Id)
		}
	}
	var counts struct {
		Seen        int `db:"seen"`
		SeenInQuery int `db:"seen_in_query"`
	}
	if err := dbManager.Db.Get(&counts, `
		SELECT COUNT(*) FILTER (WHERE found) AS seen,
		       COUNT(*) FILTER (WHERE found AND id::text = ANY($4)) AS seen_in_query
		FROM (
		    SELECT id, position($3 in regexp_replace(lower(COALESCE(response, '')), '\s+', ' ', 'g')) > 0 AS found
		    FROM llm_conversation_tool_calls
		    WHERE message_id = $1 AND tool_name = $2 AND status = 'success') t`,
		messageId, tool, normalizeText(expect), pq.Array(matched)); err != nil {
		return nil, nil, nil
	}
	s, sq := counts.Seen > 0, counts.SeenInQuery > 0
	return &r, &s, &sq
}

// queryMatchesCall reports whether a stored query names the same command as a
// recorded tool call. The investigation rewrites its own command when it writes
// the block — reordered flags, {window_start} in place of a timestamp, a
// shortened pipeline — so this matches on distinctive tokens rather than
// equality. Three quarters of them must appear in the call's parameters.
func queryMatchesCall(query, parameters string) bool {
	params := strings.ToLower(parameters)
	if strings.TrimSpace(query) == "" || params == "" {
		return false
	}
	tokens := distinctiveTokens(query)
	if len(tokens) < 3 {
		// Too little to match on (an agent-style tool given a one-line question).
		// Fall back to containment either way round.
		q := normalizeText(query)
		return strings.Contains(normalizeText(params), q) || strings.Contains(q, normalizeText(params))
	}
	hits := 0
	for _, t := range tokens {
		if strings.Contains(params, t) {
			hits++
		}
	}
	return hits*4 >= len(tokens)*3
}

// distinctiveTokens returns the lowercased tokens of a command that identify it:
// at least three characters, not a time-window placeholder, not a word that
// appears in every command of its kind.
func distinctiveTokens(query string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 8)
	for _, tok := range commandTokenPattern.FindAllString(strings.ToLower(query), -1) {
		tok = strings.Trim(tok, "-_./:")
		if len(tok) < 3 || seen[tok] || commandFillerWords[tok] || strings.HasPrefix(tok, "{window") {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// normalizeText lowercases and collapses whitespace so an expect or a command
// compares the same however the investigation spaced it.
func normalizeText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
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
	check.ToolRanInRun, check.ExpectSeenInRun, check.ExpectSeenInQuery = toolEvidenceInMessage(dbManager, messageId, check.Tool, check.Query, check.Expect)
	check.RecordedAt = time.Now().UTC()
	common.MetricsEventAnalysisOperationsTotal("confirming_check", "produced", accountId)
	ctx.GetLogger().Info("analyzer: confirming check recorded",
		"event_id", eventId, "tool", check.Tool,
		"tool_ran_in_run", boolOrUnknown(check.ToolRanInRun),
		"expect_seen_in_run", boolOrUnknown(check.ExpectSeenInRun),
		"expect_seen_in_query", boolOrUnknown(check.ExpectSeenInQuery),
		"volatile_value", check.VolatileValue,
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
