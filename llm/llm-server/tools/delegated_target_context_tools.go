package tools

// These backend query tools take no target argument of their own: they read
// QueryConfig.DelegatedTarget from the call context and query that target's
// account (executeFetchLogs, executeFetchLogsCanonical, executeFetchTrace and
// PrometheusExecuteTool.Call). A targeted child may therefore use them.

func (PrometheusExecuteTool) SupportsDelegatedTarget() bool         { return true }
func (*NBLogToolV2) SupportsDelegatedTarget() bool                  { return true }
func (SignozExecuteTool) SupportsDelegatedTarget() bool             { return true }
func (DatadogLogExecuteTool) SupportsDelegatedTarget() bool         { return true }
func (TracesExecuteChronosphereTool) SupportsDelegatedTarget() bool { return true }
func (TracesExecuteClickhouseTool) SupportsDelegatedTarget() bool   { return true }
func (TracesExecuteDefaultTool) SupportsDelegatedTarget() bool      { return true }
func (TracesExecuteJaegerTool) SupportsDelegatedTarget() bool       { return true }
func (DatadogTracesExecuteTool) SupportsDelegatedTarget() bool      { return true }

// Events tools query the delegated target's account through
// TargetDataAccountID / targetDataContext; evidence files stay in the
// conversation's workspace.
func (GetEventByIdTool) SupportsDelegatedTarget() bool         { return true }
func (ListEventsTool) SupportsDelegatedTarget() bool           { return true }
func (AggregateEventsTool) SupportsDelegatedTarget() bool      { return true }
func (EventsExecuteTool) SupportsDelegatedTarget() bool        { return true }
func (AnomalyExecuteTool) SupportsDelegatedTarget() bool       { return true }
func (GetEventEvidenceTool) SupportsDelegatedTarget() bool     { return true }
func (TriageExplanationTool) SupportsDelegatedTarget() bool    { return true }
func (TriageRulesTool) SupportsDelegatedTarget() bool          { return true }
func (ThresholdSuggestionsTool) SupportsDelegatedTarget() bool { return true }
func (TriageDryRunTool) SupportsDelegatedTarget() bool         { return true }
func (EventRulesTool) SupportsDelegatedTarget() bool           { return true }
func (EventClassificationTool) SupportsDelegatedTarget() bool  { return true }
func (TriageRuleEventsTool) SupportsDelegatedTarget() bool     { return true }
func (IncidentAssemblyTool) SupportsDelegatedTarget() bool     { return true }

// FinOps read tools default to the delegated target's account through
// targetDataContext (recommendation_execute also takes a per-call target).
// The recommendation write tools stay refused under another account.
func (SpendSummaryTool) SupportsDelegatedTarget() bool                    { return true }
func (SpendForecastTool) SupportsDelegatedTarget() bool                   { return true }
func (SpendAllocationTool) SupportsDelegatedTarget() bool                 { return true }
func (RecommendationResolutionExecuteTool) SupportsDelegatedTarget() bool { return true }
func (ProposeRecommendationApplyTool) SupportsDelegatedTarget() bool      { return true }
