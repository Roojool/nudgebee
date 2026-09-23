package core

import (
	"context"
	"fmt"
	"nudgebee/llm/security"
	toolcore "nudgebee/llm/tools/core"
	"strings"

	"github.com/samber/lo"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/prompts"
)

// multiOptionSeparator joins the options of a multi-select classification answer.
// Both the instruction given to the model and the canonical response use it.
const multiOptionSeparator = ", "

// ClassificationPlanner is a planner that classifies input against a set of options.
type ClassificationPlanner struct {
	ctx     *security.RequestContext
	prompt  prompts.FormatPrompter
	request NBAgentRequest
	nbAgent NBAgent
	options []string
	// multi allows the answer to name several options, comma-separated. Off by
	// default: a single-select agent must keep rejecting a multi-option answer.
	multi bool
}

// NewClassificationPlanner creates a new classification planner. When multi is
// true the model may answer with several options separated by ", "; every one of
// them is still validated against the options list, so a multi-select answer can
// no more invent a name than a single-select one can.
func NewClassificationPlanner(
	ctx *security.RequestContext,
	request NBAgentRequest,
	nbAgent NBAgent,
	systemMessage string,
	options []string,
	multi bool,
) (*ClassificationPlanner, error) {
	if len(options) == 0 {
		return nil, fmt.Errorf("at least one option must be provided for classification")
	}

	return &ClassificationPlanner{
		ctx:     ctx,
		prompt:  createClassificationPrompt(systemMessage, options, multi),
		request: request,
		nbAgent: nbAgent,
		options: options,
		multi:   multi,
	}, nil
}

// validateAnswer canonicalizes the model's raw answer and reports whether it is
// usable. Single-select: the whole answer must be one option. Multi-select: every
// comma-separated token must be an option, and the canonical form re-joins them in
// the order the model gave — the first is the one the caller treats as primary, so
// the order carries meaning and must not be sorted.
//
// An answer is rejected wholesale if any token is unknown, rather than dropping the
// bad token: a model that invented one name has misread the alert, and silently
// keeping its other guesses would launder that into a confident-looking answer.
func (p *ClassificationPlanner) validateAnswer(raw string) (string, bool) {
	answer := strings.TrimSpace(raw)
	if !p.multi {
		for _, opt := range p.options {
			if strings.EqualFold(opt, answer) {
				return opt, true // canonical casing
			}
		}
		return answer, false
	}

	var chosen []string
	seen := make(map[string]bool, len(p.options))
	for _, token := range strings.Split(answer, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		canonical := ""
		for _, opt := range p.options {
			if strings.EqualFold(opt, token) {
				canonical = opt
				break
			}
		}
		if canonical == "" {
			return answer, false // unknown option — reject the whole answer
		}
		if seen[canonical] {
			continue // the model listed the same option twice
		}
		seen[canonical] = true
		chosen = append(chosen, canonical)
	}
	if len(chosen) == 0 {
		return answer, false
	}
	return strings.Join(chosen, multiOptionSeparator), true
}

func createClassificationPrompt(systemMessage string, options []string, multi bool) prompts.ChatPromptTemplate {
	optionsStr := "- " + strings.Join(options, "\n- ")

	strictInstructions := "You MUST choose one of the options from the list provided. Your response MUST be ONLY the name of the chosen option. Do not add any other text, explanation, or punctuation. Do not make up new options."
	if multi {
		// Kept deliberately close to the single-select wording: the only licence
		// granted is naming more than one option, and only options from the list.
		strictInstructions = "You MUST choose one or more of the options from the list provided. Your response MUST be ONLY the chosen option names, separated by a comma and a space when there is more than one. Do not add any other text, explanation, or punctuation. Do not make up new options."
	}
	fullSystemMessage := fmt.Sprintf("%s\n\n%s\n\nYour options are:\n%s", systemMessage, strictInstructions, optionsStr)

	messageFormatters := []prompts.MessageFormatter{
		prompts.NewSystemMessagePromptTemplate(fullSystemMessage, nil),
		prompts.NewHumanMessagePromptTemplate("{{.input}}", []string{"input"}),
	}

	tmpl := prompts.NewChatPromptTemplate(messageFormatters)
	tmpl.PartialVariables = map[string]any{}
	return tmpl
}

// Plan decides which option to select based on the input.
func (p *ClassificationPlanner) Plan(
	ctx context.Context,
	intermediateSteps []NBAgentPlannerToolActionStep,
	input string,
) ([]NBAgentPlannerToolAction, *NBAgentPlannerFinishAction, error) {
	logger := p.ctx.GetLogger().With("agent", p.nbAgent.GetName(), "agent_id", p.request.AgentId)
	logger.Debug("classification_planner: planning", "input", input)

	fullInputs := map[string]any{"input": input}

	prompt, err := p.prompt.FormatPrompt(fullInputs)
	if err != nil {
		return nil, nil, err
	}

	mcList := lo.Map(prompt.Messages(), func(msg llms.ChatMessage, _ int) llms.MessageContent {
		return llms.MessageContent{
			Role:  msg.GetType(),
			Parts: []llms.ContentPart{llms.TextContent{Text: msg.GetContent()}},
		}
	})

	// Honour the agent's declared cache scope, mirroring the ReAct planners.
	// GenerateAndTrackLLMContent reads ContextKeyCacheScope from the context and
	// defaults to Conversation scope when it is absent, so without this an agent's
	// GetCacheScope() would have no effect on the classification path.
	llmCtx := p.ctx
	if cacheProvider, ok := p.nbAgent.(NBAgentCacheScopeProvider); ok {
		llmCtx = security.NewRequestContext(
			context.WithValue(p.ctx.GetContext(), ContextKeyCacheScope, cacheProvider.GetCacheScope()),
			p.ctx.GetSecurityContext(),
			p.ctx.GetLogger(),
			p.ctx.GetTracer(),
			p.ctx.GetMeter(),
		)
	}

	var chosenOption string
	var isValid bool

	for i := range 3 {
		result, err := GenerateAndTrackLLMContent(llmCtx, p.request.UserId, p.request.AccountId, p.request.ConversationId, p.request.MessageId, p.request.ParentAgentId, true, mcList, true, llms.WithTemperature(0.0), WithThinkingLevel(ThinkingLevelFastTask))
		if err != nil {
			logger.Error("classification_planner: unable to process llm request", "error", err, "attempt", i+1)
			return nil, nil, err
		}

		if len(result.Choices) == 0 || result.Choices[0].Content == "" {
			return nil, nil, fmt.Errorf("no content in response from LLM")
		}

		chosenOption, isValid = p.validateAnswer(result.Choices[0].Content)

		if isValid {
			break // Success
		}

		// If not valid and not the last attempt, prepare for retry
		if i < 2 {
			logger.Warn("classification_planner: LLM returned an invalid option, retrying...", "returned_option", chosenOption, "attempt", i+1)

			// Add AI's invalid response to history
			mcList = append(mcList, llms.MessageContent{
				Role:  llms.ChatMessageTypeAI,
				Parts: []llms.ContentPart{llms.TextContent{Text: result.Choices[0].Content}},
			})

			// Add a new instruction for the LLM. Multi-select gets its own wording so
			// the retry doesn't force a multi-select agent back to single-select.
			var retryPrompt string
			if p.multi {
				retryPrompt = fmt.Sprintf(
					"Your response '%s' contains invalid options. You MUST choose one or more of the options from the list provided, separated by a comma and a space. Do not add any other text to your response.",
					chosenOption,
				)
			} else {
				retryPrompt = fmt.Sprintf(
					"Your response '%s' is not a valid option. You MUST choose one of the options from the list provided. Do not add any other text to your response.",
					chosenOption,
				)
			}
			mcList = append(mcList, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextContent{Text: retryPrompt}},
			})
		}
	}

	if !isValid {
		err := fmt.Errorf("LLM returned an invalid option after 3 attempts: '%s'. Valid options are: %v", chosenOption, p.options)
		logger.Error("classification_planner: failed after retries", "error", err)
		return nil, nil, err
	}

	logger.Debug("classification_planner: finished", "chosen_option", chosenOption)

	return nil, &NBAgentPlannerFinishAction{
		Data:   chosenOption,
		Status: ConversationStatusCompleted,
	}, nil
}

// GetTools returns the tools for the planner.
func (p *ClassificationPlanner) GetTools() []toolcore.NBTool {
	// This planner does not use tools.
	return []toolcore.NBTool{}
}

func (p *ClassificationPlanner) Marshal() ([]byte, error) {
	return nil, nil
}

func (p *ClassificationPlanner) Unmarshal([]byte) error {
	return nil
}
