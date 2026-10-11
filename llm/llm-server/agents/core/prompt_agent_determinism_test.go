package core

import (
	"fmt"
	"strings"
	"testing"

	toolcore "nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The system prompt must be byte-identical every time it is built for the same
// agent and tools: a difference anywhere changes the explicit cache's content
// hash and cuts Gemini's implicit prefix cache at that point (#39633). Go
// randomizes map iteration, so both tests build many times and compare.

const promptBuilds = 50

func TestGetPromptTemplate_ToolUsageIsDeterministicAndSorted(t *testing.T) {
	usage := map[string][]string{}
	for i := range 12 {
		usage[fmt.Sprintf("tool_%02d", 11-i)] = []string{fmt.Sprintf("use tool %d", i)}
	}
	p := NBAgentPrompt{Role: "a test agent", ToolUsage: usage}

	first := GetPromptTemplate(p, NBAgentRequest{}, AgentPlannerTypeReAct).Template
	for range promptBuilds {
		require.Equal(t, first, GetPromptTemplate(p, NBAgentRequest{}, AgentPlannerTypeReAct).Template)
	}

	prev := -1
	for i := range 12 {
		idx := strings.Index(first, fmt.Sprintf("**tool_%02d**", i))
		require.Greater(t, idx, prev, "tool_%02d out of order", i)
		prev = idx
	}
}

// optionalArgsTool has several optional arguments and none required, which is
// the branch that shows one optional argument as an example.
type optionalArgsTool struct{ mockStandardTool }

func (optionalArgsTool) Name() string { return "optional_args_tool" }
func (optionalArgsTool) InputSchema() toolcore.ToolSchema {
	props := map[string]toolcore.ToolSchemaProperty{}
	for _, name := range []string{"zeta", "alpha", "mu", "delta", "omega", "kappa"} {
		props[name] = toolcore.ToolSchemaProperty{Type: toolcore.ToolSchemaTypeString, Description: name}
	}
	return toolcore.ToolSchema{Type: toolcore.ToolSchemaTypeObject, Properties: props}
}

func TestUpdatePromptForMultiCommandTool_OptionalExampleIsDeterministic(t *testing.T) {
	build := func() []string {
		p, err := UpdatePromptForMultiCommandTool(NBAgentRequest{}, []toolcore.NBTool{optionalArgsTool{}}, NBAgentPrompt{})
		require.NoError(t, err)
		return p.ToolUsage["optional_args_tool"]
	}

	first := build()
	for range promptBuilds {
		require.Equal(t, first, build())
	}
	assert.Contains(t, strings.Join(first, "\n"), `"alpha": "<string>"`, "the first optional argument by name is the example")
}
