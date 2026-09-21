package watch

import (
	"regexp"
	"strings"
)

// untrustedOpen / untrustedClose delimit observation text inside an LLM
// prompt. An explicit tag rather than a markdown rule ("---"): a rule is
// forgeable by any command whose output happens to contain one, which is not
// hypothetical for `kubectl describe` or a log tail.
const (
	untrustedOpen  = "<untrusted_observation>"
	untrustedClose = "</untrusted_observation>"
)

// untrustedTagRx matches anything a model is liable to read as one of our
// frame tags: either direction, any case, incidental inner whitespace.
//
// Matching the literal bytes we emit would be the wrong invariant. The frame
// is interpreted by a model, not parsed, and a model treats
// `</UNTRUSTED_OBSERVATION>` as the closing tag as readily as the exact form.
var untrustedTagRx = regexp.MustCompile(`(?i)<\s*/?\s*untrusted_observation\s*>`)

// WrapUntrusted frames command output for inclusion in an LLM prompt.
//
// A watch observation is not operator-authored text. It is whatever the polled
// command printed, and parts of that are routinely controlled by other people:
// pod names and annotations in `kubectl get` output, workflow names from a
// repository, log lines echoing user input. Two prompts consume it — the
// llm_judge predicate, whose answer decides whether a watch terminates, and the
// terminal summarizer, whose output is appended to the user's chat thread.
// Injected text in either makes the watch lie about a mutating action's outcome.
func WrapUntrusted(observation string) string {
	// Both directions are neutralised: a closing tag would end the frame early,
	// an opening one would suggest a second frame began.
	escaped := untrustedTagRx.ReplaceAllStringFunc(observation, func(tag string) string {
		if strings.Contains(tag, "/") {
			return "&lt;/untrusted_observation&gt;"
		}
		return "&lt;untrusted_observation&gt;"
	})
	return untrustedOpen + "\n" + escaped + "\n" + untrustedClose
}
