package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsContextExtractionApplicable(t *testing.T) {
	// Headless automated pipelines — context extraction must be skipped
	// to avoid token inflation and redundant distillation runs.
	// Assert against raw string literals to guard against accidental constant mutations.
	assert.False(t, isContextExtractionApplicable("Investigation"),
		"Investigation source is automated headless alert analysis")
	assert.False(t, isContextExtractionApplicable("Automation"),
		"Automation source is automated scheduled workflow execution")
	assert.False(t, isContextExtractionApplicable("pr_lifecycle"),
		"pr_lifecycle source is automated PR comment reviewer bot")
	assert.False(t, isContextExtractionApplicable("recommendation"),
		"recommendation source is automated rightsizing PR generator")

	// Human interactive sessions — context extraction must remain enabled
	// so multi-turn context carries forward.
	assert.True(t, isContextExtractionApplicable("UserInvestigation"),
		"UserInvestigation is interactive human console chat")
	assert.True(t, isContextExtractionApplicable("InstantNotification"),
		"InstantNotification is interactive Slack chat thread")
	assert.True(t, isContextExtractionApplicable("WorkflowBuilder"),
		"WorkflowBuilder is interactive UI assistant")
	assert.True(t, isContextExtractionApplicable("Optimize"),
		"Optimize is interactive UI assistant")
	assert.True(t, isContextExtractionApplicable("UserInvestigationCli"),
		"UserInvestigationCli is interactive CLI session")
	assert.True(t, isContextExtractionApplicable("PrometheusQuery"))
	assert.True(t, isContextExtractionApplicable("LogQuery"))
	assert.True(t, isContextExtractionApplicable(""), "Empty source defaults to true for safety")

	// Constant identity parity check against canonical string values
	assert.Equal(t, ConversationSource("Investigation"), ConversationSourceInvestigation)
	assert.Equal(t, ConversationSource("Automation"), ConversationSourceAutomation)
	assert.Equal(t, ConversationSource("pr_lifecycle"), ConversationSourcePRLifecycle)
	assert.Equal(t, ConversationSource("recommendation"), ConversationSourceRecommendation)
	assert.Equal(t, ConversationSource("UserInvestigation"), ConversationSourceUserInvestigation)
	assert.Equal(t, ConversationSource("InstantNotification"), ConversationSourceInstantNotification)
	assert.Equal(t, ConversationSource("WorkflowBuilder"), ConversationSourceWorkflowBuilder)
	assert.Equal(t, ConversationSource("Optimize"), ConversationSourceOptimize)
	assert.Equal(t, ConversationSource("UserInvestigationCli"), ConversationSourceUserInvestigationCLI)
	assert.Equal(t, ConversationSource("PrometheusQuery"), ConversationSourcePrometheusQuery)
	assert.Equal(t, ConversationSource("LogQuery"), ConversationSourceLogQuery)
}
