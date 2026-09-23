package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClassificationPlanner_ValidateAnswer covers the answer canonicalization for
// both selection modes. Multi-select must widen what the model may report (several
// options) without widening what it may invent (anything off the list).
func TestClassificationPlanner_ValidateAnswer(t *testing.T) {
	options := []string{"cart-service", "checkout-api", "Payment-Gateway", "Not Found"}

	t.Run("single-select", func(t *testing.T) {
		p := &ClassificationPlanner{options: options, multi: false}

		tests := []struct {
			name      string
			raw       string
			want      string
			wantValid bool
		}{
			{"exact option", "cart-service", "cart-service", true},
			{"surrounding whitespace is trimmed", "  cart-service\n", "cart-service", true},
			{"casing is canonicalized to the option list", "payment-gateway", "Payment-Gateway", true},
			{"the Not Found sentinel is a valid choice", "Not Found", "Not Found", true},
			{"invented option is rejected", "billing-service", "billing-service", false},
			{"prose around the option is rejected", "The answer is cart-service", "The answer is cart-service", false},
			{"empty answer is rejected", "", "", false},
			// The whole point of leaving multi off by default: a single-select
			// caller can only consume one name, so it must not silently take the
			// first of several.
			{"multi-option answer is rejected when multi is off", "cart-service, checkout-api", "cart-service, checkout-api", false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, valid := p.validateAnswer(tt.raw)
				assert.Equal(t, tt.wantValid, valid)
				assert.Equal(t, tt.want, got)
			})
		}
	})

	t.Run("multi-select", func(t *testing.T) {
		p := &ClassificationPlanner{options: options, multi: true}

		tests := []struct {
			name      string
			raw       string
			want      string
			wantValid bool
		}{
			{"single option still works unchanged", "cart-service", "cart-service", true},
			{"two options", "cart-service, checkout-api", "cart-service, checkout-api", true},
			{"separator is normalized to comma-space", "cart-service,checkout-api", "cart-service, checkout-api", true},
			{"per-token casing is canonicalized", "cart-service, payment-gateway", "cart-service, Payment-Gateway", true},
			{"ragged whitespace is tolerated", " cart-service ,   checkout-api ", "cart-service, checkout-api", true},
			{"duplicates are collapsed", "cart-service, cart-service", "cart-service", true},
			{"trailing comma is ignored", "cart-service,", "cart-service", true},
			// Order is the contract: api-server makes the first name the subject.
			{"model's order is preserved, not sorted", "checkout-api, cart-service", "checkout-api, cart-service", true},
			// One invented name means the model misread the alert; keeping its
			// other guesses would launder that into a confident-looking answer.
			{"one invented option rejects the whole answer", "cart-service, billing-service", "cart-service, billing-service", false},
			{"all-invented answer is rejected", "billing-service, shipping-service", "billing-service, shipping-service", false},
			{"empty answer is rejected", "", "", false},
			{"separators only is rejected", " , , ", ", ,", false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, valid := p.validateAnswer(tt.raw)
				assert.Equal(t, tt.wantValid, valid)
				assert.Equal(t, tt.want, got)
			})
		}
	})
}

// TestNewClassificationPlanner_MultiInstruction asserts the model is told which
// mode it is in: the single-select wording ("choose one of the options") would
// actively contradict an agent that wants several names back.
func TestNewClassificationPlanner_MultiInstruction(t *testing.T) {
	options := []string{"cart-service", "Not Found"}

	single := createClassificationPrompt("system", options, false)
	singleMsgs, err := single.FormatMessages(map[string]any{"input": "alert"})
	assert.NoError(t, err)
	assert.Contains(t, singleMsgs[0].GetContent(), "You MUST choose one of the options")
	assert.NotContains(t, singleMsgs[0].GetContent(), "one or more")

	multi := createClassificationPrompt("system", options, true)
	multiMsgs, err := multi.FormatMessages(map[string]any{"input": "alert"})
	assert.NoError(t, err)
	assert.Contains(t, multiMsgs[0].GetContent(), "You MUST choose one or more of the options")
	assert.Contains(t, multiMsgs[0].GetContent(), "separated by a comma and a space")
	// Both modes must keep forbidding invented options.
	assert.Contains(t, multiMsgs[0].GetContent(), "Do not make up new options")
}
