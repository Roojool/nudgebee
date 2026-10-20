package common

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// ProviderHTTPError is a non-2xx response from an LLM provider, carrying the
// rate-limit headers that came with it.
//
// Those headers are the only reliable answer to "was this a request limit or a
// token limit, and what is the ceiling". Discarding them means every throttling
// incident has to be reconstructed by hand from call timestamps, which cannot
// distinguish the two: when every call requests an identical max_tokens, a
// request limiter and a token limiter produce the same cutoff.
//
// Error() reproduces the exact string the clients used to build by hand, because
// the retry and fallback classifiers (isQuotaError, isTokenLimitError,
// isTransientError) match on error text. Changing the wording here silently
// reclassifies live failures.
type ProviderHTTPError struct {
	StatusCode int
	// Body is the provider's error message, empty when the payload did not decode.
	Body      string
	RateLimit map[string]string
}

func (e *ProviderHTTPError) Error() string {
	msg := fmt.Sprintf("API returned unexpected status code: %d", e.StatusCode)
	if e.Body == "" {
		return msg
	}
	return fmt.Sprintf("%s: %s", msg, e.Body)
}

// NewProviderHTTPError builds the error for a non-2xx response. body may be empty
// when the provider's error payload did not decode.
//
// Takes the status and headers rather than the *http.Response so it cannot be
// mistaken for a function that consumes the body — closing it stays the caller's
// job, right where the response is read.
func NewProviderHTTPError(statusCode int, header http.Header, body string) *ProviderHTTPError {
	return &ProviderHTTPError{
		StatusCode: statusCode,
		Body:       body,
		RateLimit:  readRateLimitHeaders(header),
	}
}

// isRateLimitHeader reports whether a response header is safe and useful to keep
// on a throttling failure. The test is name-based and deliberately narrow: a
// header must announce itself as a rate limit (or a retry delay) to be captured,
// so credentials living in other headers can never be reflected into a log.
//
// Matching on the name rather than an exact allowlist is the point — the limit
// that prompted this is enforced by a gateway whose header naming we do not
// control, and an allowlist would silently capture nothing.
func isRateLimitHeader(name string) bool {
	n := strings.ToLower(name)

	// Naming a rate limit is not on its own enough to be safe: a gateway can
	// carry a rate-limit *bypass* credential in a header that matches the name
	// rule (X-RateLimit-Bypass-Key, RateLimit-Signature). Reject anything that
	// announces itself as a credential. "token" is deliberately absent — the
	// legitimate counters are x-ratelimit-{limit,remaining}-tokens.
	for _, banned := range []string{"key", "secret", "signature", "auth", "bearer", "credential", "password", "bypass", "cookie"} {
		if strings.Contains(n, banned) {
			return false
		}
	}

	if n == "retry-after" || n == "retry-after-ms" {
		return true
	}
	return strings.Contains(n, "ratelimit") || strings.Contains(n, "rate-limit")
}

// maxRateLimitHeaderValueLen caps what a single header can contribute to a log
// line. Real values are short counters and timestamps; anything longer is a
// broken or hostile gateway and must not be copied into logs unbounded.
const maxRateLimitHeaderValueLen = 128

func readRateLimitHeaders(h http.Header) map[string]string {
	var out map[string]string
	for name, values := range h {
		if !isRateLimitHeader(name) || len(values) == 0 {
			continue
		}
		// First value only: these headers are single-valued by definition, and a
		// repeated one means a confused gateway rather than extra information.
		value := values[0]
		if value == "" {
			continue // present but empty says nothing; logging it is pure noise
		}
		if len(value) > maxRateLimitHeaderValueLen {
			value = TruncateHead(value, maxRateLimitHeaderValueLen) + "…(truncated)"
		}
		if out == nil {
			out = make(map[string]string, 4)
		}
		out[strings.ToLower(name)] = value
	}
	return out
}

// rateLimitHeaderError attaches a response's rate-limit headers to an error
// whose message is built elsewhere. Clients word their errors differently
// (anthropic: "anthropic: API error (429 rate_limit_error): ..."), and the retry
// and fallback classifiers match on that text, so rewording them to share
// ProviderHTTPError would silently reclassify live failures. This carries the
// headers alongside the original error instead of replacing it.
type rateLimitHeaderError struct {
	err        error
	statusCode int
	rateLimit  map[string]string
}

func (e *rateLimitHeaderError) Error() string { return e.err.Error() }
func (e *rateLimitHeaderError) Unwrap() error { return e.err }

// WithRateLimitHeaders returns err carrying the response's rate-limit headers.
// The message is unchanged. Returns err untouched when there is nothing to add,
// so call sites can wrap unconditionally.
func WithRateLimitHeaders(err error, statusCode int, header http.Header) error {
	if err == nil {
		return nil
	}
	headers := readRateLimitHeaders(header)
	if len(headers) == 0 && statusCode != http.StatusTooManyRequests {
		return err
	}
	return &rateLimitHeaderError{err: err, statusCode: statusCode, rateLimit: headers}
}

// rateLimitInfoFrom pulls the status, headers and classifiable text out of
// whichever carrier err is, so both shapes behave identically downstream.
func rateLimitInfoFrom(err error) (statusCode int, headers map[string]string, text string, ok bool) {
	var provErr *ProviderHTTPError
	if errors.As(err, &provErr) {
		return provErr.StatusCode, provErr.RateLimit, provErr.Body, true
	}
	var wrapped *rateLimitHeaderError
	if errors.As(err, &wrapped) {
		return wrapped.statusCode, wrapped.rateLimit, wrapped.Error(), true
	}
	return 0, nil, "", false
}

// RateLimitHeadersFromError returns the rate-limit headers carried by err, if any.
func RateLimitHeadersFromError(err error) (map[string]string, bool) {
	_, headers, _, ok := rateLimitInfoFrom(err)
	if !ok || len(headers) == 0 {
		return nil, false
	}
	return headers, true
}

// RateLimitLogAttrs renders the headers as slog key/value pairs, sorted so the
// same failure always logs in the same order. Returns nil when err carries none,
// which lets call sites append unconditionally.
func RateLimitLogAttrs(err error) []any {
	headers, ok := RateLimitHeadersFromError(err)
	if !ok {
		// No headers, but the body may still name the limit — that is exactly the
		// case this was built for, so it must not drop out here.
		if detail := RateLimitDetailFromError(err); detail.Known() {
			return []any{"rate_limit_scope", string(detail.Scope), "rate_limit_window", string(detail.Window)}
		}
		return nil
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	attrs := make([]any, 0, len(names)*2+4)
	for _, name := range names {
		attrs = append(attrs, name, headers[name])
	}
	if detail := RateLimitDetailFromError(err); detail.Known() {
		attrs = append(attrs, "rate_limit_scope", string(detail.Scope), "rate_limit_window", string(detail.Window))
	}
	return attrs
}

// RateLimitScope is what the provider said was exhausted.
type RateLimitScope string

const (
	RateLimitScopeRequests RateLimitScope = "requests"
	RateLimitScopeTokens   RateLimitScope = "tokens"
	RateLimitScopeUnknown  RateLimitScope = "unknown"
)

// RateLimitWindow is the period the exhausted limit is measured over.
type RateLimitWindow string

const (
	RateLimitWindowMinute  RateLimitWindow = "minute"
	RateLimitWindowHour    RateLimitWindow = "hour"
	RateLimitWindowDay     RateLimitWindow = "day"
	RateLimitWindowUnknown RateLimitWindow = "unknown"
)

// RateLimitDetail says which limit a 429 exhausted. The window is the part worth
// having: a per-minute limit clears on its own in seconds, a per-day limit does
// not clear today at all, so the same "please wait a moment" advice is right for
// one and actively misleading for the other.
//
// Both fields stay Unknown unless the provider actually said — nothing here
// guesses, because a wrong window sends the user (or an operator) to the wrong
// remedy, which is worse than admitting we don't know.
type RateLimitDetail struct {
	Scope  RateLimitScope
	Window RateLimitWindow
}

// Known reports whether the provider told us anything usable.
func (d RateLimitDetail) Known() bool {
	return d.Scope != RateLimitScopeUnknown || d.Window != RateLimitWindowUnknown
}

// UnknownRateLimitDetail is the zero value callers use when no provider signal is
// available — a plain RateLimitDetail{} would read as scope "" rather than unknown.
func UnknownRateLimitDetail() RateLimitDetail {
	return RateLimitDetail{Scope: RateLimitScopeUnknown, Window: RateLimitWindowUnknown}
}

// Acronyms name both halves at once and are the most specific signal, so they are
// matched first. Word-bounded: "rpm" must not match inside an unrelated word.
// Window phrases, matched whole-word so a short form cannot hit inside an
// unrelated word. Providers abbreviate: OpenAI writes "requests per min (RPM)",
// not "per minute", and that spelling is only rescued today because the acronym
// happens to be alongside it. Compiled once — these run on every 429.
var (
	windowDayRe    = regexp.MustCompile(`\bper[- ]?days?\b|\bdaily\b`)
	windowHourRe   = regexp.MustCompile(`\bper[- ]?hours?\b|\bhourly\b`)
	windowMinuteRe = regexp.MustCompile(`\bper[- ]?min(ute)?s?\b`)
)

var rateLimitAcronyms = []struct {
	re     *regexp.Regexp
	detail RateLimitDetail
}{
	{regexp.MustCompile(`\brpm\b`), RateLimitDetail{RateLimitScopeRequests, RateLimitWindowMinute}},
	{regexp.MustCompile(`\brpd\b`), RateLimitDetail{RateLimitScopeRequests, RateLimitWindowDay}},
	{regexp.MustCompile(`\brph\b`), RateLimitDetail{RateLimitScopeRequests, RateLimitWindowHour}},
	{regexp.MustCompile(`\btpm\b`), RateLimitDetail{RateLimitScopeTokens, RateLimitWindowMinute}},
	{regexp.MustCompile(`\btpd\b`), RateLimitDetail{RateLimitScopeTokens, RateLimitWindowDay}},
}

// RateLimitDetailFromError reports which limit err exhausted, reading the
// provider's own signals: the response headers first (structured, and a remaining
// counter of exactly 0 names the exhausted dimension outright), then the error
// body, which is often the only place the window appears.
func RateLimitDetailFromError(err error) RateLimitDetail {
	statusCode, headers, text, ok := rateLimitInfoFrom(err)
	if !ok {
		return UnknownRateLimitDetail()
	}
	return rateLimitDetailFrom(statusCode, headers, text)
}

func rateLimitDetailFrom(statusCode int, rateLimitHeaders map[string]string, rawText string) RateLimitDetail {
	detail := UnknownRateLimitDetail()

	// Only a 429 describes a rate limit. Without this gate any response whose
	// body happens to mention requests or tokens picks up a reason it never
	// had: a context-window 400 ("maximum context length is 32768 tokens")
	// reads as a token limit and stamps rate_limit_scope=tokens on a failure
	// that has nothing to do with throttling.
	if statusCode != http.StatusTooManyRequests {
		return detail
	}

	body := strings.ToLower(rawText)

	// Acronyms are the only signal naming both halves at once, so they seed the
	// answer — but they are prose, and a body naming two different limits does not
	// say which was hit. Nothing is returned yet: headers outrank prose below.
	var matched []RateLimitDetail
	for _, a := range rateLimitAcronyms {
		if a.re.MatchString(body) {
			matched = append(matched, a.detail)
		}
	}
	if len(matched) == 1 {
		detail = matched[0]
	}

	// Headers are the ground truth for the dimension: a remaining counter of
	// exactly zero is a machine-generated fact about what ran out, where the body
	// may be a template that names a limit the account did not actually hit. This
	// overrides an acronym rather than being skipped by one. If both counters are
	// zero the response is genuinely ambiguous, so neither is claimed.
	requestsOut := remainingIsExhausted(rateLimitHeaders, "requests")
	tokensOut := remainingIsExhausted(rateLimitHeaders, "tokens")
	switch {
	case requestsOut && !tokensOut:
		detail.Scope = RateLimitScopeRequests
	case tokensOut && !requestsOut:
		detail.Scope = RateLimitScopeTokens
	}

	if detail.Scope == RateLimitScopeUnknown {
		detail.Scope = scopeFromBody(body)
	}

	// Only prose carries the window — a reset header is a delay, not a window
	// size. An unambiguous acronym already set it; otherwise look for a phrase.
	if detail.Window == RateLimitWindowUnknown {
		switch {
		case windowDayRe.MatchString(body):
			detail.Window = RateLimitWindowDay
		case windowHourRe.MatchString(body):
			detail.Window = RateLimitWindowHour
		case windowMinuteRe.MatchString(body):
			detail.Window = RateLimitWindowMinute
		}
	}

	return detail
}

func containsAny(body string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(body, phrase) {
			return true
		}
	}
	return false
}

// scopeFromBody reads the exhausted dimension out of a provider's prose.
//
// Phrases that actually qualify a limit are checked before bare word mentions,
// because a message routinely names the other dimension in passing: Azure's
// token-limit 429 opens "Requests to the ChatCompletions_Create Operation have
// exceeded token rate limit...", where a bare search for "request" wins and
// reports the wrong limit. Ambiguous messages return Unknown rather than pick.
func scopeFromBody(body string) RateLimitScope {
	tokenPhrase := containsAny(body, "token rate limit", "token limit", "tokens per", "tokens-per", "token quota")
	requestPhrase := containsAny(body, "request rate limit", "request limit", "requests per", "requests-per", "request quota")
	switch {
	case tokenPhrase && !requestPhrase:
		return RateLimitScopeTokens
	case requestPhrase && !tokenPhrase:
		return RateLimitScopeRequests
	}

	// No qualifying phrase: a message naming only one of the two words means that
	// one. Naming both without qualifying either is not enough to decide.
	//
	// "Too many requests" is stripped first: it is the standard HTTP reason phrase
	// for every 429, returned verbatim for token-limit rejections too, so reading
	// a request limit out of it would be inventing a dimension the provider never
	// named.
	body = strings.ReplaceAll(body, "too many requests", "")
	hasToken := strings.Contains(body, "token")
	hasRequest := strings.Contains(body, "request")
	switch {
	case hasToken && !hasRequest:
		return RateLimitScopeTokens
	case hasRequest && !hasToken:
		return RateLimitScopeRequests
	}
	return RateLimitScopeUnknown
}

// remainingIsExhausted reports whether a "remaining <dimension>" counter reached
// zero, matching on the parts of the name rather than an exact key.
//
// Real gateways split and order these differently — Azure-style deployments send
// x-ratelimit-remaining-input-tokens and -output-tokens, and anthropic reverses
// it to anthropic-ratelimit-requests-remaining. An exact-key lookup for
// x-ratelimit-remaining-tokens silently matches none of them, which costs the
// dimension on exactly the responses whose body says nothing useful.
func remainingIsExhausted(headers map[string]string, dimension string) bool {
	for name, value := range headers {
		if value != "0" {
			continue
		}
		if strings.Contains(name, "remaining") && strings.Contains(name, dimension) {
			return true
		}
	}
	return false
}
