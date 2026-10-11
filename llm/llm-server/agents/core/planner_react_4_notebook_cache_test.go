package core

import (
	"fmt"
	"testing"
	"time"

	toolcore "nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
)

// An update_notebook call during a turn must not rewrite the start of the
// prompt: everything after a changed message is re-billed by provider prefix
// caching (#39633). The prompt for step N+1 must begin with the exact prompt
// sent for step N.

func notebookTestPlanner(initial string) *NBReActPlanner4 {
	return &NBReActPlanner4{
		request:       NBAgentRequest{AgentId: "a", AccountId: "acc-1"},
		systemMessage: "system prompt",
		Notebook:      initial,
	}
}

func kubectlStep(i int) NBAgentPlannerToolActionStep {
	return NBAgentPlannerToolActionStep{
		Action:      NBAgentPlannerToolAction{Tool: "kubectl", ToolInput: `{"command":"get pods"}`, ToolID: fmt.Sprintf("k%d", i), TurnID: fmt.Sprintf("t%d", i)},
		Observation: fmt.Sprintf("pods %d", i),
		Status:      ToolStatusSuccess,
	}
}

func notebookStep(i int, content string) NBAgentPlannerToolActionStep {
	return NBAgentPlannerToolActionStep{
		Action:      NBAgentPlannerToolAction{Tool: toolcore.NotebookToolName, ToolInput: fmt.Sprintf(`{"content":%q}`, content), ToolID: fmt.Sprintf("n%d", i), TurnID: fmt.Sprintf("t%d", i)},
		Observation: content,
		Status:      ToolStatusSuccess,
	}
}

func TestReAct4_NotebookUpdateKeepsPromptAppendOnly(t *testing.T) {
	o := notebookTestPlanner("seeded notebook")
	steps := []NBAgentPlannerToolActionStep{kubectlStep(0)}

	o.refreshNotebookFromSteps(steps)
	before := o.buildMessages("why is checkout slow?", steps)

	steps = append(steps, notebookStep(1, "H1 confirmed"), kubectlStep(2))
	o.refreshNotebookFromSteps(steps)
	after := o.buildMessages("why is checkout slow?", steps)

	require.Greater(t, len(after), len(before))
	assert.Equal(t, before, after[:len(before)], "the earlier prompt must be an exact prefix of the later one")
	assert.Equal(t, "H1 confirmed", o.Notebook, "current state is still tracked for persistence and critique")
}

func TestReAct4_HumanMessageShowsTurnStartNotebook(t *testing.T) {
	o := notebookTestPlanner("seeded notebook")
	first := o.humanText("q")

	o.refreshNotebookFromSteps([]NBAgentPlannerToolActionStep{notebookStep(0, "updated mid-turn")})

	assert.Equal(t, first, o.humanText("q"), "an in-turn update does not change the human message")
	assert.Contains(t, first, "seeded notebook")
	assert.NotContains(t, o.humanText("q"), "updated mid-turn")
}

func TestReAct4_FailedNotebookUpdateKeepsItsError(t *testing.T) {
	o := notebookTestPlanner("")
	failed := NBAgentPlannerToolActionStep{
		Action:      NBAgentPlannerToolAction{Tool: toolcore.NotebookToolName, ToolInput: `{"content":""}`, ToolID: "n0", TurnID: "t0"},
		Observation: "notebook content was empty",
		Status:      ToolStatusFailure,
	}
	msgs := o.renderStepsToMessages([]NBAgentPlannerToolActionStep{failed})

	require.Len(t, msgs, 2)
	assert.Equal(t, "notebook content was empty", msgs[1].Parts[0].(llms.ToolCallResponse).Content,
		"only successful updates are shortened; errors stay visible to the model")
}

func TestReAct4_AppendNotebookUpdateReplay(t *testing.T) {
	o := notebookTestPlanner("")
	step := NBAgentPlannerToolActionStep{
		Action:      NBAgentPlannerToolAction{Tool: toolcore.NotebookToolName, ToolInput: `{"content":"new evidence","append":true}`, ToolID: "n0", TurnID: "t0"},
		Observation: "new evidence",
		Status:      ToolStatusSuccess,
	}
	msgs := o.renderStepsToMessages([]NBAgentPlannerToolActionStep{step})

	require.Len(t, msgs, 2)
	assert.Equal(t, notebookAppendedReplay, msgs[1].Parts[0].(llms.ToolCallResponse).Content)
}

// A mid-turn suspend/resume (e.g. write approval) builds a new planner. Its
// human message must match the pre-suspend one, or the resumed prompt no longer
// extends the cached prefix.
func TestReAct4_TurnStartNotebookSurvivesResume(t *testing.T) {
	o := notebookTestPlanner("seeded notebook")
	before := o.humanText("q")
	o.refreshNotebookFromSteps([]NBAgentPlannerToolActionStep{notebookStep(0, "updated mid-turn")})

	state, err := o.Marshal()
	require.NoError(t, err)
	resumed := notebookTestPlanner("")
	require.NoError(t, resumed.Unmarshal(state))
	resumed.refreshNotebookFromSteps([]NBAgentPlannerToolActionStep{notebookStep(0, "updated mid-turn")})

	assert.Equal(t, before, resumed.humanText("q"), "timestamp and notebook block survive the resume")
	assert.Equal(t, "updated mid-turn", resumed.Notebook, "current state is restored for persistence and critique")
}

// A new turn restored from the previous turn's state must render the notebook
// and time as they stand now, not the prior turn's snapshot; mid-turn (steps
// present) the snapshot is kept.
func TestReAct4_FreshTurnResetsTurnStartState(t *testing.T) {
	o := notebookTestPlanner("old notebook")
	o.turnStartedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	o.humanText("q")
	o.Notebook = "new notebook"

	o.resetTurnStateOnFreshTurn([]NBAgentPlannerToolActionStep{kubectlStep(0)})
	assert.Contains(t, o.humanText("q"), "old notebook", "mid-turn keeps the snapshot")

	o.resetTurnStateOnFreshTurn(nil)
	text := o.humanText("q")
	assert.Contains(t, text, "new notebook")
	assert.NotContains(t, text, "2020", "a new turn takes a new timestamp")
}
