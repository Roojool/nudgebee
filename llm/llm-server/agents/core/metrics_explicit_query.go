package core

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	metricsSearchIndex = regexp.MustCompile(`(?im)^\s*GET\s+([^\s/]+)/_search\b`)
	metricsNamedIndex  = regexp.MustCompile(`(?i)\bindex\s+([a-zA-Z0-9_.:*-]+)`)
	metricsExecuteDSL  = regexp.MustCompile(`(?i)\b(?:execute|run)\s+(?:this|the|following)\b`)
	metricsNoExecute   = regexp.MustCompile(`(?i)\b(?:do not|don't|without)\s+(?:execute|run)\b`)
)

// explicitMetricsQuery accepts only a complete DSL body and an explicit index
// from the user's message. KB text and inferred schema fields are never used.
// The existing metrics tool remains responsible for validation and index access.
func explicitMetricsQuery(message string) string {
	if !strings.Contains(message, "{") {
		return ""
	}
	executeRequested := false
	for _, match := range metricsExecuteDSL.FindAllStringIndex(message, -1) {
		start := match[0] - 16
		if start < 0 {
			start = 0
		}
		prefix := strings.ToLower(strings.TrimSpace(message[start:match[0]]))
		if strings.HasSuffix(prefix, "do not") || strings.HasSuffix(prefix, "don't") || strings.HasSuffix(prefix, "without") {
			continue
		}
		executeRequested = true
	}
	searchRequested := metricsSearchIndex.MatchString(message) && !metricsNoExecute.MatchString(message)
	rawEnvelope := strings.HasPrefix(strings.TrimSpace(message), "{")
	if !executeRequested && !searchRequested && !rawEnvelope {
		return ""
	}
	index := ""
	indexMatches := metricsSearchIndex.FindAllStringSubmatch(message, -1)
	if len(indexMatches) == 0 {
		indexMatches = metricsNamedIndex.FindAllStringSubmatch(message, -1)
	}
	for _, match := range indexMatches {
		candidate := strings.TrimRight(match[1], ".,;:")
		if index != "" && index != candidate {
			return "" // Do not pair a query with an ambiguous index.
		}
		index = candidate
	}
	selected := ""
	for pos := 0; pos < len(message); pos++ {
		if message[pos] != '{' {
			continue
		}
		var obj map[string]any
		decoder := json.NewDecoder(strings.NewReader(message[pos:]))
		decoder.UseNumber()
		if err := decoder.Decode(&obj); err != nil || obj == nil {
			continue
		}
		// The decoded object already includes its nested clauses. Continue after it
		// so inner query objects are not mistaken for separate DSL bodies.
		pos += int(decoder.InputOffset()) - 1
		blockIndex := index
		body := obj
		if suppliedIndex, ok := obj["index"].(string); ok {
			blockIndex = suppliedIndex
			var valid bool
			body, valid = obj["query"].(map[string]any)
			if !valid {
				continue
			}
		}
		if blockIndex == "" {
			continue
		}
		if _, ok := body["query"].(map[string]any); !ok {
			continue
		}
		result, err := json.Marshal(map[string]any{"index": blockIndex, "query": body})
		if err == nil {
			if selected != "" && selected != string(result) {
				return "" // Multiple distinct DSL bodies: let the agent clarify intent.
			}
			selected = string(result)
		}
	}
	return selected
}

// Execute the first metrics call with the user's DSL. NativeToolInput retains
// the model's signed call; renderObservation names the actual executed input
// when those differ. Later calls can correct a rejected query normally.
func (o *NBReActPlanner4) useFirstExplicitMetricsQuery(actions []NBAgentPlannerToolAction, prior []NBAgentPlannerToolActionStep) {
	for _, step := range prior {
		if step.Action.Tool == "es_metrics_query" {
			return
		}
	}
	userMessage := o.request.QueryConfig.OriginalUserQuery
	if userMessage == "" {
		userMessage = o.request.Query
	}
	query := explicitMetricsQuery(userMessage)
	if query == "" {
		return
	}
	for i := range actions {
		if actions[i].Tool != "es_metrics_query" {
			continue
		}
		actions[i].ToolInput = resolveToolInputMacros(query)
		if o.ctx != nil {
			o.ctx.GetLogger().Info("react4: executing user-supplied metrics DSL on first metrics call", "agent", o.agentName())
		}
		return
	}
}
