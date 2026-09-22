package common

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hdr(headers map[string]string) http.Header {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return h
}

// The retry/fallback classifiers match on error text, so the wording the clients
// used to build by hand has to survive verbatim.
func TestProviderHTTPErrorPreservesLegacyMessage(t *testing.T) {
	body := "Rate limit is exceeded. This deployment has reached its configured requests-per-minute (RPM) limit."
	err := NewProviderHTTPError(429, nil, body)
	legacy := fmt.Sprintf("%s: %s", "API returned unexpected status code: 429", body)
	assert.Equal(t, legacy, err.Error())

	// Undecodable payload: status code alone, as errors.New(msg) produced before.
	assert.Equal(t, "API returned unexpected status code: 500", NewProviderHTTPError(500, nil, "").Error())
}

func TestRateLimitHeadersCapturedAndScoped(t *testing.T) {
	err := NewProviderHTTPError(429, hdr(map[string]string{
		"Retry-After":                  "17",
		"x-ratelimit-limit-requests":   "10",
		"X-RateLimit-Remaining-Tokens": "0",
		"RateLimit-Reset":              "42",
		"Authorization":                "Bearer super-secret",
		"Set-Cookie":                   "session=secret",
		"Content-Type":                 "application/json",
		"x-request-id":                 "req_123",
	}), "throttled")

	headers, ok := RateLimitHeadersFromError(err)
	require.True(t, ok)

	assert.Equal(t, map[string]string{
		"retry-after":                  "17",
		"x-ratelimit-limit-requests":   "10",
		"x-ratelimit-remaining-tokens": "0",
		"ratelimit-reset":              "42",
	}, headers)

	// Anything not naming itself a rate limit must never be reflected into a log.
	for _, name := range []string{"authorization", "set-cookie", "content-type", "x-request-id"} {
		_, present := headers[name]
		assert.False(t, present, "header %q must not be captured", name)
	}
}

func TestRateLimitLogAttrsSortedAndNilSafe(t *testing.T) {
	err := NewProviderHTTPError(429, hdr(map[string]string{
		"x-ratelimit-remaining-tokens":   "0",
		"retry-after":                    "5",
		"x-ratelimit-remaining-requests": "0",
	}), "throttled")

	assert.Equal(t, []any{
		"retry-after", "5",
		"x-ratelimit-remaining-requests", "0",
		"x-ratelimit-remaining-tokens", "0",
	}, RateLimitLogAttrs(err))

	// Call sites append unconditionally, so the no-headers cases must yield nil.
	assert.Nil(t, RateLimitLogAttrs(NewProviderHTTPError(500, nil, "boom")))
	assert.Nil(t, RateLimitLogAttrs(errors.New("not a provider error")))
	assert.Nil(t, RateLimitLogAttrs(nil))
}

// The error travels up wrapped; the headers have to survive the unwrap chain.
func TestRateLimitHeadersSurviveWrapping(t *testing.T) {
	err := NewProviderHTTPError(429, hdr(map[string]string{"Retry-After": "3"}), "throttled")
	wrapped := fmt.Errorf("react4: llm generate: %w", fmt.Errorf("agent failed: %w", err))

	headers, ok := RateLimitHeadersFromError(wrapped)
	require.True(t, ok)
	assert.Equal(t, "3", headers["retry-after"])
}

func TestRateLimitDetailFromBody(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		scope  RateLimitScope
		window RateLimitWindow
	}{
		// The message from the 2026-09-21 llm-server incident, verbatim.
		{
			name:   "gateway RPM message",
			body:   "Rate limit is exceeded. This deployment has reached its configured requests-per-minute (RPM) limit. Please retry shortly.",
			scope:  RateLimitScopeRequests,
			window: RateLimitWindowMinute,
		},
		{
			name:   "azure token rate limit",
			body:   "Requests to the ChatCompletions_Create Operation have exceeded token rate limit of your current OpenAI S0 pricing tier.",
			scope:  RateLimitScopeTokens,
			window: RateLimitWindowUnknown,
		},
		{
			name:   "daily request cap",
			body:   "You exceeded your current quota: requests per day (RPD) limit reached.",
			scope:  RateLimitScopeRequests,
			window: RateLimitWindowDay,
		},
		{
			name:   "spelled out daily limit",
			body:   "Your daily request allowance has been used up.",
			scope:  RateLimitScopeRequests,
			window: RateLimitWindowDay,
		},
		{
			name:   "tokens per minute acronym",
			body:   "TPM limit reached for this deployment.",
			scope:  RateLimitScopeTokens,
			window: RateLimitWindowMinute,
		},
		{
			name:   "hourly cap",
			body:   "Too many requests per hour.",
			scope:  RateLimitScopeRequests,
			window: RateLimitWindowHour,
		},
		{
			// The standard 429 reason phrase names no dimension — Azure returns it
			// verbatim for token-limit rejections too.
			name:   "bare HTTP reason phrase infers nothing",
			body:   "Too many requests.",
			scope:  RateLimitScopeUnknown,
			window: RateLimitWindowUnknown,
		},
		{
			name:   "reason phrase plus a real signal still resolves",
			body:   "429 Too Many Requests: token rate limit exceeded",
			scope:  RateLimitScopeTokens,
			window: RateLimitWindowUnknown,
		},
		{
			name:   "entirely opaque",
			body:   "429",
			scope:  RateLimitScopeUnknown,
			window: RateLimitWindowUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail := RateLimitDetailFromError(NewProviderHTTPError(429, nil, tc.body))
			assert.Equal(t, tc.scope, detail.Scope)
			assert.Equal(t, tc.window, detail.Window)
		})
	}
}

// A remaining counter of exactly zero names the exhausted dimension outright, so
// it is trusted ahead of guessing from prose.
func TestRateLimitDetailFromHeaders(t *testing.T) {
	requestsOut := NewProviderHTTPError(429, hdr(map[string]string{
		"x-ratelimit-remaining-requests": "0",
		"x-ratelimit-remaining-tokens":   "48000",
	}), "rate limit reached")
	assert.Equal(t, RateLimitScopeRequests, RateLimitDetailFromError(requestsOut).Scope)

	tokensOut := NewProviderHTTPError(429, hdr(map[string]string{
		"x-ratelimit-remaining-requests": "8",
		"x-ratelimit-remaining-tokens":   "0",
	}), "rate limit reached")
	assert.Equal(t, RateLimitScopeTokens, RateLimitDetailFromError(tokensOut).Scope)

	// Both exhausted is genuinely ambiguous — claiming either would be a guess.
	bothOut := NewProviderHTTPError(429, hdr(map[string]string{
		"x-ratelimit-remaining-requests": "0",
		"x-ratelimit-remaining-tokens":   "0",
	}), "limit reached")
	assert.Equal(t, RateLimitScopeUnknown, RateLimitDetailFromError(bothOut).Scope)
}

func TestRateLimitDetailUnknownForNonProviderErrors(t *testing.T) {
	detail := RateLimitDetailFromError(errors.New("connection refused"))
	assert.Equal(t, RateLimitScopeUnknown, detail.Scope)
	assert.Equal(t, RateLimitWindowUnknown, detail.Window)
	assert.False(t, detail.Known())
}

// The body alone is often the only place the window appears, so a provider that
// sends no rate-limit headers must still produce the reason attrs.
func TestRateLimitLogAttrsFromBodyWithoutHeaders(t *testing.T) {
	err := NewProviderHTTPError(429, nil, "requests-per-minute (RPM) limit reached")
	assert.Equal(t, []any{
		"rate_limit_scope", "requests",
		"rate_limit_window", "minute",
	}, RateLimitLogAttrs(err))
}

// Only a 429 describes a rate limit. Any other status whose body happens to
// mention requests or tokens must not pick up a reason — a context-window 400
// says "maximum context length is 32768 tokens" and has nothing to do with
// throttling, but would otherwise be logged as rate_limit_scope=tokens.
func TestRateLimitDetailOnlyDerivedFor429(t *testing.T) {
	body := "requests-per-minute (RPM) limit reached"
	for _, status := range []int{200, 400, 401, 403, 418, 500, 502, 503} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			detail := RateLimitDetailFromError(NewProviderHTTPError(status, nil, body))
			assert.False(t, detail.Known(), "status %d must carry no rate-limit reason", status)
			assert.Nil(t, RateLimitLogAttrs(NewProviderHTTPError(status, nil, body)))
		})
	}

	assert.True(t, RateLimitDetailFromError(NewProviderHTTPError(429, nil, body)).Known())
}

func TestRateLimitDetailRefusesConflictingSignals(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		scope  RateLimitScope
		window RateLimitWindow
	}{
		{"two different acronyms", "rpm and tpd both exceeded", RateLimitScopeUnknown, RateLimitWindowUnknown},
		{"two qualifying phrases", "requests per minute and tokens per minute both exhausted", RateLimitScopeUnknown, RateLimitWindowMinute},
		{"both bare words", "your request used too many token credits", RateLimitScopeUnknown, RateLimitWindowUnknown},
		{"one acronym only", "tpd exceeded", RateLimitScopeTokens, RateLimitWindowDay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail := RateLimitDetailFromError(NewProviderHTTPError(429, nil, tc.body))
			assert.Equal(t, tc.scope, detail.Scope)
			assert.Equal(t, tc.window, detail.Window)
		})
	}
}

// Naming a rate limit is not on its own enough to be safe to log: a gateway can
// carry a bypass credential in a header that matches the name rule.
func TestRateLimitHeadersRejectCredentialNames(t *testing.T) {
	err := NewProviderHTTPError(429, hdr(map[string]string{
		"X-RateLimit-Bypass-Key":         "super-secret-bypass-value",
		"RateLimit-Signature":            "deadbeefcafe",
		"X-RateLimit-Auth":               "Bearer abc123",
		"X-RateLimit-Credential":         "cred",
		"X-RateLimit-Secret":             "shh",
		"X-RateLimit-Remaining-Requests": "0",
		"X-RateLimit-Remaining-Tokens":   "0",
	}), "limit")

	headers, ok := RateLimitHeadersFromError(err)
	require.True(t, ok)

	// The legitimate counters survive — "token" must not be treated as a secret word.
	assert.Equal(t, map[string]string{
		"x-ratelimit-remaining-requests": "0",
		"x-ratelimit-remaining-tokens":   "0",
	}, headers)

	for _, v := range []string{"super-secret-bypass-value", "deadbeefcafe", "Bearer abc123", "cred", "shh"} {
		assert.NotContains(t, fmt.Sprint(RateLimitLogAttrs(err)), v)
	}
}

func TestRateLimitHeaderValuesAreBounded(t *testing.T) {
	long := strings.Repeat("A", 5000)
	err := NewProviderHTTPError(429, hdr(map[string]string{"X-RateLimit-Reset-Requests": long}), "limit")

	headers, ok := RateLimitHeadersFromError(err)
	require.True(t, ok)
	got := headers["x-ratelimit-reset-requests"]
	assert.LessOrEqual(t, len(got), maxRateLimitHeaderValueLen+len("…(truncated)"))
	assert.Contains(t, got, "(truncated)")
}

// Header values are nominally ASCII but nothing enforces that on the wire, and
// cutting at a byte index can land inside a multi-byte character — the log line
// the cap exists to protect then carries replacement junk.
func TestRateLimitHeaderTruncationNeverSplitsARune(t *testing.T) {
	// Each ✓ is 3 bytes, so a byte-index cut at 128 lands mid-rune.
	value := strings.Repeat("✓", 200)
	err := NewProviderHTTPError(429, hdr(map[string]string{"X-RateLimit-Reset-Requests": value}), "limit")

	headers, ok := RateLimitHeadersFromError(err)
	require.True(t, ok)
	got := headers["x-ratelimit-reset-requests"]

	assert.True(t, utf8.ValidString(got), "truncated value must stay valid UTF-8: %q", got)
	assert.NotContains(t, got, "\uFFFD", "no replacement characters")
	assert.Contains(t, got, "(truncated)")
	assert.LessOrEqual(t, len(got), maxRateLimitHeaderValueLen+len("…(truncated)"))
}

func TestRateLimitHeaderOddities(t *testing.T) {
	t.Run("empty value is dropped as noise", func(t *testing.T) {
		err := NewProviderHTTPError(429, hdr(map[string]string{
			"Retry-After":                    "",
			"X-RateLimit-Remaining-Requests": "4",
		}), "limit")
		headers, _ := RateLimitHeadersFromError(err)
		_, present := headers["retry-after"]
		assert.False(t, present)
		assert.Equal(t, "4", headers["x-ratelimit-remaining-requests"])
	})

	t.Run("repeated header takes the first value", func(t *testing.T) {
		h := http.Header{}
		h["X-Ratelimit-Remaining-Requests"] = []string{"0", "5", "9"}
		headers, _ := RateLimitHeadersFromError(NewProviderHTTPError(429, h, "limit"))
		assert.Equal(t, "0", headers["x-ratelimit-remaining-requests"])
	})

	t.Run("nil header map is safe", func(t *testing.T) {
		err := NewProviderHTTPError(429, nil, "requests-per-minute (RPM) limit")
		_, ok := RateLimitHeadersFromError(err)
		assert.False(t, ok)
		// The body still resolves the reason.
		assert.Equal(t, RateLimitScopeRequests, RateLimitDetailFromError(err).Scope)
	})

	t.Run("non-canonical casing is normalised", func(t *testing.T) {
		h := http.Header{}
		h["X-RATELIMIT-REMAINING-TOKENS"] = []string{"0"}
		headers, _ := RateLimitHeadersFromError(NewProviderHTTPError(429, h, "limit"))
		assert.Equal(t, "0", headers["x-ratelimit-remaining-tokens"])
	})

	t.Run("empty body on a 429 is safe", func(t *testing.T) {
		err := NewProviderHTTPError(429, nil, "")
		assert.Equal(t, "API returned unexpected status code: 429", err.Error())
		assert.False(t, RateLimitDetailFromError(err).Known())
	})
}

// Clients whose error wording differs (anthropic, vertex, huggingface) carry the
// headers via WithRateLimitHeaders instead of ProviderHTTPError. Both shapes must
// behave identically downstream, and the original message must be untouched —
// the retry classifiers match on it.
func TestWithRateLimitHeadersKeepsTheMessage(t *testing.T) {
	original := errors.New("anthropic: API error (429 rate_limit_error): Number of requests has exceeded your per-minute rate limit.")
	wrapped := WithRateLimitHeaders(original, 429, hdr(map[string]string{
		"Retry-After":                    "8",
		"X-RateLimit-Remaining-Requests": "0",
		"Authorization":                  "Bearer nope",
	}))

	assert.Equal(t, original.Error(), wrapped.Error(), "message must not change")
	assert.ErrorIs(t, wrapped, original, "original error identity must survive")

	headers, ok := RateLimitHeadersFromError(wrapped)
	require.True(t, ok)
	assert.Equal(t, "8", headers["retry-after"])
	_, leaked := headers["authorization"]
	assert.False(t, leaked)

	detail := RateLimitDetailFromError(wrapped)
	assert.Equal(t, RateLimitScopeRequests, detail.Scope)
	assert.Equal(t, RateLimitWindowMinute, detail.Window)
}

func TestWithRateLimitHeadersEdgeCases(t *testing.T) {
	t.Run("nil error stays nil", func(t *testing.T) {
		assert.Nil(t, WithRateLimitHeaders(nil, 429, hdr(map[string]string{"Retry-After": "1"})))
	})

	t.Run("non-429 with no headers is returned untouched", func(t *testing.T) {
		original := errors.New("boom")
		assert.Equal(t, original, WithRateLimitHeaders(original, 500, nil))
	})

	t.Run("non-429 acquires no reason even with headers", func(t *testing.T) {
		wrapped := WithRateLimitHeaders(errors.New("boom"), 500,
			hdr(map[string]string{"X-RateLimit-Remaining-Requests": "0"}))
		assert.False(t, RateLimitDetailFromError(wrapped).Known())
	})

	t.Run("429 with no headers still classifies from the message", func(t *testing.T) {
		wrapped := WithRateLimitHeaders(errors.New("requests per day (RPD) exceeded"), 429, nil)
		detail := RateLimitDetailFromError(wrapped)
		assert.Equal(t, RateLimitScopeRequests, detail.Scope)
		assert.Equal(t, RateLimitWindowDay, detail.Window)
	})

	t.Run("survives further wrapping", func(t *testing.T) {
		wrapped := WithRateLimitHeaders(errors.New("tpm limit"), 429, hdr(map[string]string{"Retry-After": "4"}))
		outer := fmt.Errorf("agent: %w", fmt.Errorf("llm: %w", wrapped))

		headers, ok := RateLimitHeadersFromError(outer)
		require.True(t, ok)
		assert.Equal(t, "4", headers["retry-after"])
		assert.Equal(t, RateLimitScopeTokens, RateLimitDetailFromError(outer).Scope)
	})
}

// Gateways split and order the remaining-counter headers differently. An
// exact-key lookup for x-ratelimit-remaining-tokens matched none of these, which
// cost the dimension on exactly the responses whose body says nothing useful.
// The azure-style and anthropic-style names here were both observed live.
func TestRemainingCounterHeaderNameVariants(t *testing.T) {
	cases := []struct {
		name   string
		header string
		scope  RateLimitScope
	}{
		{"openai style requests", "X-RateLimit-Remaining-Requests", RateLimitScopeRequests},
		{"openai style tokens", "X-RateLimit-Remaining-Tokens", RateLimitScopeTokens},
		{"split input tokens", "X-RateLimit-Remaining-Input-Tokens", RateLimitScopeTokens},
		{"split output tokens", "X-RateLimit-Remaining-Output-Tokens", RateLimitScopeTokens},
		{"reversed order requests", "Anthropic-RateLimit-Requests-Remaining", RateLimitScopeRequests},
		{"reversed order tokens", "Anthropic-RateLimit-Tokens-Remaining", RateLimitScopeTokens},
		{"rfc draft style", "RateLimit-Remaining-Requests", RateLimitScopeRequests},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Body deliberately says nothing, so only the header can resolve this.
			err := NewProviderHTTPError(429, hdr(map[string]string{tc.header: "0"}), "limit reached")
			assert.Equal(t, tc.scope, RateLimitDetailFromError(err).Scope)
		})
	}

	t.Run("a non-zero counter names nothing", func(t *testing.T) {
		err := NewProviderHTTPError(429, hdr(map[string]string{"X-RateLimit-Remaining-Input-Tokens": "5"}), "limit reached")
		assert.Equal(t, RateLimitScopeUnknown, RateLimitDetailFromError(err).Scope)
	})

	t.Run("both dimensions exhausted stays ambiguous", func(t *testing.T) {
		err := NewProviderHTTPError(429, hdr(map[string]string{
			"X-RateLimit-Remaining-Requests":     "0",
			"X-RateLimit-Remaining-Input-Tokens": "0",
		}), "limit reached")
		assert.Equal(t, RateLimitScopeUnknown, RateLimitDetailFromError(err).Scope)
	})
}

// Providers abbreviate the window, and short forms matched as bare substrings
// hit inside unrelated words. These are matched whole-word instead.
func TestRateLimitWindowPhraseVariants(t *testing.T) {
	cases := []struct {
		body   string
		window RateLimitWindow
	}{
		// Abbreviated forms — OpenAI writes "per min", not "per minute".
		{"rate limit: 500 requests per min", RateLimitWindowMinute},
		{"rate limit: 500 requests per-min", RateLimitWindowMinute},
		{"rate limit: 500 requests per minute", RateLimitWindowMinute},
		{"rate limit: 500 requests per-minute", RateLimitWindowMinute},
		{"limit of 500 requests per mins", RateLimitWindowMinute},

		{"quota of 200 requests per day", RateLimitWindowDay},
		{"quota of 200 requests per-day", RateLimitWindowDay},
		{"your daily request quota is spent", RateLimitWindowDay},

		{"only 50 requests per hour allowed", RateLimitWindowHour},
		{"the hourly request cap was reached", RateLimitWindowHour},

		// Whole-word matching: none of these name a window.
		{"the permanent request limit was reached", RateLimitWindowUnknown},
		{"superminute request budget exhausted", RateLimitWindowUnknown},
		{"request limit reached for this deployment", RateLimitWindowUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.body[:min(len(tc.body), 34)], func(t *testing.T) {
			assert.Equal(t, tc.window, RateLimitDetailFromError(NewProviderHTTPError(429, nil, tc.body)).Window)
		})
	}
}

// The window must resolve from prose alone, with no acronym to fall back on —
// that is the case the abbreviated spellings exist for.
func TestWindowResolvesWithoutAnAcronym(t *testing.T) {
	err := NewProviderHTTPError(429, nil, "rate limit exceeded: 500 requests per min for this deployment")
	detail := RateLimitDetailFromError(err)
	assert.Equal(t, RateLimitScopeRequests, detail.Scope)
	assert.Equal(t, RateLimitWindowMinute, detail.Window)
}

// Headers outrank prose for the dimension. A remaining counter of exactly zero
// is a machine-generated fact about what ran out; the body may be a template
// naming a limit the account did not actually hit. Before this, an acronym in
// the body returned early and the headers were never consulted.
func TestHeadersOutrankBodyAcronyms(t *testing.T) {
	t.Run("conflicting acronyms still resolve from headers", func(t *testing.T) {
		// Two acronyms say nothing on their own, but the header is unambiguous.
		err := NewProviderHTTPError(429,
			hdr(map[string]string{"X-RateLimit-Remaining-Requests": "0"}),
			"rpm and tpd limits both configured")
		assert.Equal(t, RateLimitScopeRequests, RateLimitDetailFromError(err).Scope)
	})

	t.Run("a header overrides a single conflicting acronym", func(t *testing.T) {
		err := NewProviderHTTPError(429,
			hdr(map[string]string{"X-RateLimit-Remaining-Tokens": "0"}),
			"rpm limit reached")
		detail := RateLimitDetailFromError(err)
		assert.Equal(t, RateLimitScopeTokens, detail.Scope, "header must win over the acronym")
		// The acronym is still the only window signal, so it survives.
		assert.Equal(t, RateLimitWindowMinute, detail.Window)
	})

	t.Run("an agreeing header changes nothing", func(t *testing.T) {
		err := NewProviderHTTPError(429,
			hdr(map[string]string{"X-RateLimit-Remaining-Requests": "0"}),
			"rpm limit reached")
		detail := RateLimitDetailFromError(err)
		assert.Equal(t, RateLimitScopeRequests, detail.Scope)
		assert.Equal(t, RateLimitWindowMinute, detail.Window)
	})

	t.Run("conflicting acronyms with no headers stay unknown", func(t *testing.T) {
		err := NewProviderHTTPError(429, nil, "rpm and tpd limits both configured")
		assert.False(t, RateLimitDetailFromError(err).Known())
	})

	t.Run("both counters exhausted stays ambiguous even with an acronym", func(t *testing.T) {
		err := NewProviderHTTPError(429, hdr(map[string]string{
			"X-RateLimit-Remaining-Requests": "0",
			"X-RateLimit-Remaining-Tokens":   "0",
		}), "rpm limit reached")
		detail := RateLimitDetailFromError(err)
		// Headers cannot decide; the acronym is the only remaining signal.
		assert.Equal(t, RateLimitScopeRequests, detail.Scope)
		assert.Equal(t, RateLimitWindowMinute, detail.Window)
	})

	t.Run("window survives when headers resolve the scope", func(t *testing.T) {
		err := NewProviderHTTPError(429,
			hdr(map[string]string{"X-RateLimit-Remaining-Requests": "0"}),
			"quota exceeded: requests per day")
		detail := RateLimitDetailFromError(err)
		assert.Equal(t, RateLimitScopeRequests, detail.Scope)
		assert.Equal(t, RateLimitWindowDay, detail.Window)
	})
}
