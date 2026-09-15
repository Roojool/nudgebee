package egressfilter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
)

// Two distinct, well-formed AWS access key ids (documentation-shaped, not real
// credentials). Both match the aws-access-key-id baseline rule.
const (
	secretPrior   = "AKIAIOSFODNN7EXAMPLE" // "pasted on turn 1"
	secretCurrent = "AKIAQQQQQQQQQQQQQQQQ" // "typed on turn 2"
)

func TestReportScope_EmptyHistoryKeepsFirstSighting(t *testing.T) {
	payload := "here is " + secretCurrent + " ok"
	hits := Scan(payload).Hits
	require.Len(t, hits, 1)

	kept, mask := newReportScope("", "").filter(hits, payload)
	assert.Len(t, kept, 1, "an empty history seed keeps the first sighting")
	assert.Equal(t, []bool{true}, mask)
}

func TestReportScope_DropsCarriedOverSecret(t *testing.T) {
	// Payload re-sends the prior secret (history) and adds a fresh one.
	scope := newReportScope("previous message pasted "+secretPrior, "")
	payload := "<history>" + secretPrior + "</history> <question>" + secretCurrent + "</question>"

	hits := Scan(payload).Hits
	require.Len(t, hits, 2, "both AWS keys should be detected in the payload")

	kept, mask := scope.filter(hits, payload)
	require.Len(t, kept, 1, "the carried-over secret must be dropped from the report")

	// The kept hit must be the current-turn secret, not the carried one.
	assert.Equal(t, secretCurrent, payload[kept[0].Start:kept[0].End])
	assert.Len(t, mask, 2)
	assert.Equal(t, len(hits), len(mask), "mask must align with the input hits")
}

func TestReportScope_AllCarriedOverYieldsEmpty(t *testing.T) {
	scope := newReportScope("history holds "+secretPrior, "")
	payload := "re-sent history: " + secretPrior

	hits := Scan(payload).Hits
	require.Len(t, hits, 1)

	kept, _ := scope.filter(hits, payload)
	assert.Empty(t, kept, "a pure carry-over payload reports nothing new")
}

func TestReportScope_DedupesRepeatedCalls(t *testing.T) {
	// The same current-turn secret scanned on two LLM calls of one turn must
	// count exactly once — the scope remembers it after the first call.
	scope := newReportScope("", "")
	payload := "question: echo " + secretCurrent
	hits := Scan(payload).Hits
	require.Len(t, hits, 1)

	kept1, _ := scope.filter(hits, payload)
	assert.Len(t, kept1, 1, "first sighting is reported")

	kept2, _ := scope.filter(Scan(payload).Hits, payload)
	assert.Empty(t, kept2, "second sighting in the same turn is suppressed")
}

func TestFilterByMask_KeepsAlignedEntries(t *testing.T) {
	reds := []Redaction{{Placeholder: "a"}, {Placeholder: "b"}, {Placeholder: "c"}}
	out := filterByMask(reds, []bool{true, false, true})
	require.Len(t, out, 2)
	assert.Equal(t, "a", out[0].Placeholder)
	assert.Equal(t, "c", out[1].Placeholder)
}

// collectEvents wires a reporter into ctx and returns the accumulated events.
func collectEvents(base context.Context, events *[]FilterEvent) context.Context {
	return WithFilterEventReporter(base, func(e FilterEvent) {
		*events = append(*events, e)
	})
}

// TestWrapper_DetectReportsOnlyCurrentTurnSecret is the end-to-end contract for
// the carry-over fix: with a baseline holding the prior-turn secret, a detect
// call whose payload contains BOTH the prior (history) and the current secret
// must forward unchanged AND report only the current-turn secret.
func TestWrapper_DetectReportsOnlyCurrentTurnSecret(t *testing.T) {
	inner := &fakeModel{respText: "ok"}
	wrapped := WrapModel(inner, "openai", "gpt-4o", true, ModeDetect)

	msgs := []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{
			llms.TextContent{Text: "history: " + secretPrior + "\nquestion: echo " + secretCurrent},
		}},
	}

	var events []FilterEvent
	ctx := collectEvents(context.Background(), &events)
	ctx = WithReportBaseline(ctx, "earlier turn contained "+secretPrior, "")

	_, err := wrapped.GenerateContent(ctx, msgs)
	require.NoError(t, err, "detect mode must not block")
	require.Equal(t, 1, inner.callCount(), "provider is still reached in detect mode")

	require.Len(t, events, 1, "exactly one per-message event")
	assert.Equal(t, 1, events[0].HitCount, "only the current-turn secret is counted")
	assert.Contains(t, events[0].RuleIDs, "aws-access-key-id")
}

// TestWrapper_DedupesAcrossCallsInTurn: a turn drives several LLM calls that
// re-scan the same user text; sharing one per-turn scope, the freshly-typed
// secret is reported once, not once per call.
func TestWrapper_DedupesAcrossCallsInTurn(t *testing.T) {
	inner := &fakeModel{respText: "ok"}
	wrapped := WrapModel(inner, "openai", "gpt-4o", true, ModeDetect)

	msgs := []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{
			llms.TextContent{Text: "question: echo " + secretCurrent},
		}},
	}

	var events []FilterEvent
	ctx := collectEvents(context.Background(), &events)
	ctx = WithReportBaseline(ctx, "", "") // fresh turn, no history

	_, err := wrapped.GenerateContent(ctx, msgs)
	require.NoError(t, err)
	_, err = wrapped.GenerateContent(ctx, msgs)
	require.NoError(t, err)

	require.Len(t, events, 1, "the secret is reported once across the turn's calls")
	assert.Equal(t, 1, events[0].HitCount)
}

// TestWrapper_DetectSuppressesPureCarryOver: when the ONLY secret in the
// payload is one already present in the baseline, no per-message event is
// emitted — the badge must not carry a stale secret into a clean turn.
func TestWrapper_DetectSuppressesPureCarryOver(t *testing.T) {
	inner := &fakeModel{respText: "ok"}
	wrapped := WrapModel(inner, "openai", "gpt-4o", true, ModeDetect)

	msgs := []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{
			llms.TextContent{Text: "history: " + secretPrior + "\nquestion: what is the weather"},
		}},
	}

	var events []FilterEvent
	ctx := collectEvents(context.Background(), &events)
	ctx = WithReportBaseline(ctx, "earlier turn contained "+secretPrior, "")

	_, err := wrapped.GenerateContent(ctx, msgs)
	require.NoError(t, err)
	require.Equal(t, 1, inner.callCount())
	assert.Empty(t, events, "a carried-over-only turn reports no new secret")
}

// TestWrapper_EnforceStillBlocksCarriedOver: security is unaffected — a
// carried-over secret re-sent in history is still egressing, so enforce mode
// must still block even though the reported event would be empty.
func TestWrapper_EnforceStillBlocksCarriedOver(t *testing.T) {
	// Register a blocking gate for the duration of this test.
	prev := ActionGate
	ActionGate = func(mode Mode, _ Result) Action {
		if mode == ModeEnforce {
			return ActionBlock
		}
		return ActionAudit
	}
	t.Cleanup(func() { ActionGate = prev })

	inner := &fakeModel{respText: "should never reach provider"}
	wrapped := WrapModel(inner, "openai", "gpt-4o", true, ModeEnforce)

	msgs := []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{
			llms.TextContent{Text: "history: " + secretPrior + "\nquestion: what is the weather"},
		}},
	}

	var events []FilterEvent
	ctx := collectEvents(context.Background(), &events)
	ctx = WithReportBaseline(ctx, "earlier turn contained "+secretPrior, "")

	_, err := wrapped.GenerateContent(ctx, msgs)
	require.Error(t, err, "carried-over secret still egresses and must be blocked")
	assert.Equal(t, 0, inner.callCount(), "provider must not be reached")
	assert.Empty(t, events, "no NEW secret this turn, so no per-message badge")
}

// TestReportScope_ReTypedSecretInNewTurnIsReported locks the contract that when
// a secret from prior conversation history is re-typed in the current turn,
// the scope recognizes the explicit current input and reports it (fixes #35811).
func TestReportScope_ReTypedSecretInNewTurnIsReported(t *testing.T) {
	// Baseline history has 1 occurrence of secretPrior.
	scope := newReportScope("history holds "+secretPrior, secretPrior)
	// Payload contains history (1 occurrence) PLUS new message with secretPrior (2nd occurrence).
	payload := "history: " + secretPrior + "\nquestion: echo " + secretPrior

	hits := Scan(payload).Hits
	require.Len(t, hits, 2, "both occurrences in payload are scanned")

	kept, mask := scope.filter(hits, payload)
	require.Len(t, kept, 1, "re-typed secret must be reported for the current turn")
	assert.Equal(t, secretPrior, payload[kept[0].Start:kept[0].End])
	assert.Equal(t, []bool{true, false}, mask, "the re-entered value is reported once regardless of payload position")
}

// TestWrapper_DetectReportsReTypedSecret: end-to-end wrapper test ensuring that
// re-typing a secret from prior history emits a FilterEvent for the current turn.
func TestWrapper_DetectReportsReTypedSecret(t *testing.T) {
	inner := &fakeModel{respText: "ok"}
	wrapped := WrapModel(inner, "openai", "gpt-4o", true, ModeDetect)

	msgs := []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{
			llms.TextContent{Text: "history: " + secretPrior + "\nquestion: echo " + secretPrior},
		}},
	}

	var events []FilterEvent
	ctx := collectEvents(context.Background(), &events)
	ctx = WithReportBaseline(ctx, "earlier turn contained "+secretPrior, secretPrior)

	_, err := wrapped.GenerateContent(ctx, msgs)
	require.NoError(t, err)
	require.Equal(t, 1, inner.callCount())

	require.Len(t, events, 1, "re-typed secret must emit a FilterEvent for this turn")
	assert.Equal(t, 1, events[0].HitCount)
	assert.Contains(t, events[0].RuleIDs, "aws-access-key-id")
}

// Retyping is known before any call; later prompt copies must not recount it.
func TestReportScope_ReTypedSecretInSubsequentCallIsReported(t *testing.T) {
	scope := newReportScope("history holds "+secretPrior, secretPrior)
	payload := "history: " + secretPrior
	kept, _ := scope.filter(Scan(payload).Hits, payload)
	require.Len(t, kept, 1)
	payload += " question: " + secretPrior
	kept, _ = scope.filter(Scan(payload).Hits, payload)
	assert.Empty(t, kept)
}

func TestWrapper_MessageScopeAcrossAgentsAndModes(t *testing.T) {
	for _, mode := range []Mode{ModeDetect, ModeEnforce} {
		for _, retyped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retyped=%t", mode, retyped), func(t *testing.T) {
				inner := &fakeModel{respText: "ok"}
				wrapped := WrapModel(inner, "openai", "gpt-4o", true, mode)
				query := "what did I ask first?"
				if retyped {
					query += " " + secretPrior
				}
				var events []FilterEvent
				ctx := collectEvents(context.Background(), &events)
				ctx = WithReportBaseline(ctx, "previous user: "+secretPrior, query)
				payload := strings.Repeat("history: "+secretPrior+"\n", 7) + query
				msgs := []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, payload)}
				for i := 0; i < 3; i++ {
					// Child queries may echo a historical value; they cannot redefine
					// what the user actually entered or reset the shared seen set.
					child := EnsureReportBaseline(context.WithValue(ctx, reportTestKey{}, i), "", secretPrior)
					_, err := wrapped.GenerateContent(child, msgs)
					if mode == ModeEnforce {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
				}
				expected := 0
				if retyped {
					expected = 1
				}
				require.Len(t, events, expected)
				if retyped {
					assert.Equal(t, 1, events[0].HitCount)
				}
				if mode == ModeEnforce {
					assert.Zero(t, inner.callCount())
				} else {
					assert.Equal(t, 3, inner.callCount())

				}
			})
		}
	}
}

type reportTestKey struct{}

func TestReportScope_ConcurrentCallsAndFreshTurn(t *testing.T) {
	ctx := WithReportBaseline(context.Background(), "", secretCurrent)
	scope := reportScopeFromContext(ctx)
	var wg sync.WaitGroup
	var count atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := secretCurrent + " " + secretCurrent
			kept, _ := scope.filter(Scan(payload).Hits, payload)
			count.Add(int32(len(kept)))
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, count.Load())
	next := WithReportBaseline(ctx, secretCurrent, secretCurrent)
	kept, _ := reportScopeFromContext(next).filter(Scan(secretCurrent).Hits, secretCurrent)
	assert.Len(t, kept, 1, "a fresh message can report a re-entered value again")
}

func TestReportScope_NewToolSecretStillReported(t *testing.T) {
	scope := newReportScope(secretPrior, "a clean user question")
	payload := strings.Repeat(secretPrior+" ", 5) + secretCurrent
	kept, _ := scope.filter(Scan(payload).Hits, payload)
	require.Len(t, kept, 1)
	assert.Equal(t, secretCurrent, payload[kept[0].Start:kept[0].End])
}
