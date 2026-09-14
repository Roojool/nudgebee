// Package branding stores per-hostname branding, merged by the frontend on top
// of the bundled house brand. It is the only place branding is configured.
//
// A partner's asset FILES still ship in a ConfigMap mount, because they need a
// filesystem the Next.js static handler can serve. Everything theme.json can
// say ABOUT those files — which one each slot points at, the palette, the
// product and assistant names, the fonts — lives here, so it changes without an
// infra PR or a deploy.
//
// An absent or disabled row renders the house brand. An unreachable database
// must never repaint a host that already loaded its row: the frontend keeps its
// last good snapshot.
package branding

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Row is one host's override, as stored.
type Row struct {
	Id                string         `json:"id" db:"id"`
	Host              string         `json:"host" db:"host"`
	Overrides         map[string]any `json:"overrides" db:"-"`
	PreviousOverrides map[string]any `json:"previous_overrides,omitempty" db:"-"`
	Enabled           bool           `json:"enabled" db:"enabled"`
	CreatedAt         string         `json:"created_at" db:"created_at"`
	UpdatedAt         string         `json:"updated_at" db:"updated_at"`
	UpdatedBy         string         `json:"updated_by,omitempty" db:"-"`
}

type UpsertRequest struct {
	Host      string         `json:"host" validate:"required"`
	Overrides map[string]any `json:"overrides" validate:"required"`
	// Enabled defaults to true on create. Pointer so "not supplied" is
	// distinguishable from an explicit false on update.
	Enabled *bool `json:"enabled"`
}

type HostRequest struct {
	Host string `json:"host" validate:"required"`
}

type ListRequest struct{}

type ListResponse struct {
	Overrides []Row `json:"overrides"`
}

type StatusResponse struct {
	Status string `json:"status"`
	Host   string `json:"host,omitempty"`
	Id     string `json:"id,omitempty"`
	Count  int    `json:"count,omitempty"`
}

// overridableKeys is the allow-list of fields a stored override may set. It
// tracks theme.json: anything the house brand file can express, a row can
// express too.
//
// Values are the kind of validation each key gets.
//
// Asset URLs are overridable, but only as SAME-ORIGIN ROOT-RELATIVE paths (see
// validateAssetURL). The mount still supplies the files — a row points each slot
// at a file that is already served, which needs no deploy. Allowing an absolute URL would let a stored row pull a partner's logo
// from an arbitrary host: brand hijack, a tracking pixel on every page, and a
// mixed-content break, none of which a reviewer would see. A path that resolves
// to nothing is merely a broken image — visible immediately, and undone with
// branding_apply_previous_override.
var overridableKeys = map[string]overrideKind{
	// Identity and copy.
	"title":         kindText,
	"assistantName": kindText,

	// Chrome and page assets.
	"logoUrl":            kindAssetURL,
	"faviconUrl":         kindAssetURL,
	"loaderUrl":          kindAssetURL,
	"signinImageUrl":     kindAssetURL,
	"signinLeftImageUrl": kindAssetURL,

	// Assistant avatars.
	"nubiIconUrl":       kindAssetURL,
	"nubiIconLightUrl":  kindAssetURL,
	"nubiIconCircleUrl": kindAssetURL,

	// Illustration set behind getBrandingAsset() in useTenantBranding.js.
	"helpbeeIconUrl":       kindAssetURL,
	"troubleshootBeeUrl":   kindAssetURL,
	"optimizeBeeUrl":       kindAssetURL,
	"k8sBeeUrl":            kindAssetURL,
	"newUserBeeUrl":        kindAssetURL,
	"securityBeeUrl":       kindAssetURL,
	"connectClusterGifUrl": kindAssetURL,

	// Structured.
	"carouselSlides": kindCarousel,
	"theme":          kindTheme,
	"colorTokens":    kindColorTokens,
	"fontRemap":      kindFontRemap,

	// Outbound email branding, read by notifications-server for the row whose
	// host matches its BASE_URL. The web app ignores it.
	"email": kindEmail,
}

type overrideKind int

const (
	kindText overrideKind = iota
	kindAssetURL
	kindCarousel
	kindTheme
	kindColorTokens
	kindFontRemap
	kindEmail
)

// rejectedKeys are theme.json fields that deliberately cannot be overridden,
// each with the reason returned to the caller. Naming them explicitly beats the
// generic "not overridable" message, because both look like ordinary theme.json
// fields and an operator would otherwise assume a typo.
var rejectedKeys = map[string]string{
	"isWhiteLabel": "isWhiteLabel is derived, not stored: a host is white-label when its override sets " +
		"title or logoUrl; setting it directly would let a host claim or disclaim white-label " +
		"independently of its brand and desynchronise the Nudgebee mascot suppression",
}

// MaxOverrideBytes bounds the serialized override. The blob is read on every
// SSR render and every /api/public/app_config call, so an unbounded document
// would be an availability problem, not just a storage one.
const MaxOverrideBytes = 64 * 1024

const maxStringLen = 256

const maxURLLen = 2048

const maxCarouselSlides = 12

// cssTokenName matches the two design-token families the frontend applies in
// useThemeProvider. Anything else is silently dropped there, so reject it here
// instead — a token that never takes effect is a support ticket.
var cssTokenName = regexp.MustCompile(`^--(nb|ds)-[a-zA-Z0-9-]+$`)

// unsafeCSSValue rejects characters that would let a stored value escape the
// context it is interpolated into. Two different sinks, so two different
// reasons:
//
//	{ } ; " \   close a CSS declaration or the injected @font-face rule —
//	            defacement, or exfiltration via a background image URL.
//	< >         close the <style> ELEMENT. getCriticalCssTokens() builds
//	            `key:value` pairs with no escaping and _document.tsx injects
//	            the result through dangerouslySetInnerHTML, so a value
//	            containing `</style><script>…` is server-rendered as script
//	            into every page on that host — including the signin page,
//	            which is pre-auth and therefore reachable by anyone who can
//	            resolve the hostname. Blocking the angle brackets here keeps a
//	            stored override from ever reaching that sink.
//
// Validated server-side as well as in the browser so a hostile write is
// rejected at the door rather than relied upon to be filtered on the way out.
var unsafeCSSValue = regexp.MustCompile(`[{};"\\<>]`)

// NormalizeHost mirrors normalizeBrandHost() in app/src/lib/serverBranding.ts:
// first entry of a comma-joined list, lower-cased, port and trailing dot
// stripped, IPv6 brackets preserved. Stored keys and lookup keys must normalize
// identically or a row silently never matches a request.
func NormalizeHost(host string) string {
	first := strings.TrimSpace(strings.Split(host, ",")[0])
	first = strings.ToLower(first)
	if first == "" {
		return ""
	}
	var hostname string
	if strings.HasPrefix(first, "[") {
		end := strings.Index(first, "]")
		if end < 0 {
			// Malformed IPv6 literal with no closing bracket. The JS side
			// slices to indexOf(']')+1, which is 0 here, yielding "". Return
			// the same rather than the raw string: a row stored under a key
			// this side normalized differently from the lookup would silently
			// never match a request.
			return ""
		}
		hostname = first[:end+1]
	} else {
		hostname = strings.Split(first, ":")[0]
	}
	return strings.TrimSuffix(hostname, ".")
}

// ValidateOverrides rejects anything the frontend would ignore or that could
// escape a CSS declaration. Returns a caller-facing error.
// allowedKeyList renders the allow-list for error messages, sorted so the
// message is stable across runs (Go map iteration order is randomised).
func allowedKeyList() string {
	keys := make([]string, 0, len(overridableKeys))
	for k := range overridableKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func ValidateOverrides(o map[string]any) error {
	if len(o) == 0 {
		return fmt.Errorf("overrides must not be empty; delete the row instead")
	}
	for k, v := range o {
		if reason, rejected := rejectedKeys[k]; rejected {
			return fmt.Errorf("field %q cannot be overridden: %s", k, reason)
		}
		kind, ok := overridableKeys[k]
		if !ok {
			return fmt.Errorf("field %q is not overridable; allowed: %s", k, allowedKeyList())
		}
		if err := validateValue(k, kind, v); err != nil {
			return err
		}
	}
	return nil
}

func validateValue(key string, kind overrideKind, v any) error {
	switch kind {
	case kindText:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", key)
		}
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("%s must not be empty", key)
		}
		if len(s) > maxStringLen {
			return fmt.Errorf("%s must be at most %d characters", key, maxStringLen)
		}
	case kindAssetURL:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", key)
		}
		return validateAssetURL(key, s)
	case kindCarousel:
		return validateCarouselSlides(v)
	case kindColorTokens:
		return validateColorTokens(v)
	case kindFontRemap:
		return validateFontRemap(v)
	case kindEmail:
		return validateEmail(v)
	case kindTheme:
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("theme must be an object")
		}
	}
	return nil
}

// validateAssetURL constrains an asset path to the app's own origin.
//
// Empty is allowed and means "unset" — theme.json itself ships
// `"signinImageUrl": ""`, and clearing an asset has to stay expressible.
func validateAssetURL(key, raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > maxURLLen {
		return fmt.Errorf("%s is too long", key)
	}
	if strings.ContainsAny(raw, "<>\"'`\\ \t\r\n") {
		return fmt.Errorf("%s contains characters that are not valid in a URL path", key)
	}
	if !strings.HasPrefix(raw, "/") {
		return fmt.Errorf(
			"%s must be a root-relative path beginning with \"/\" (e.g. /branding/acme/logo.svg); "+
				"absolute and scheme-relative URLs are refused so a stored row cannot source a partner's "+
				"imagery from another host", key)
	}
	if strings.HasPrefix(raw, "//") {
		return fmt.Errorf("%s must not begin with \"//\" — that is a scheme-relative URL pointing at another host", key)
	}
	if strings.Contains(raw, "..") {
		return fmt.Errorf("%s must not contain \"..\"", key)
	}
	return nil
}

func validateCarouselSlides(v any) error {
	arr, ok := v.([]any)
	if !ok {
		return fmt.Errorf("carouselSlides must be an array")
	}
	if len(arr) > maxCarouselSlides {
		return fmt.Errorf("carouselSlides must have at most %d entries", maxCarouselSlides)
	}
	for i, entry := range arr {
		e, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("carouselSlides[%d] must be an object", i)
		}
		for k := range e {
			if k != "title" && k != "image" {
				return fmt.Errorf("carouselSlides[%d] has unexpected field %q; allowed: image, title", i, k)
			}
		}
		if t, present := e["title"]; present {
			ts, ok := t.(string)
			if !ok {
				return fmt.Errorf("carouselSlides[%d].title must be a string", i)
			}
			if len(ts) > maxStringLen {
				return fmt.Errorf("carouselSlides[%d].title is too long", i)
			}
		}
		if img, present := e["image"]; present {
			is, ok := img.(string)
			if !ok {
				return fmt.Errorf("carouselSlides[%d].image must be a string", i)
			}
			if err := validateAssetURL(fmt.Sprintf("carouselSlides[%d].image", i), is); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateColorTokens(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("colorTokens must be an object")
	}
	for name, val := range m {
		if !cssTokenName.MatchString(name) {
			return fmt.Errorf("colorTokens key %q must be a --nb-* or --ds-* custom property", name)
		}
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("colorTokens[%q] must be a string", name)
		}
		if unsafeCSSValue.MatchString(s) {
			return fmt.Errorf("colorTokens[%q] contains characters that are not valid in a CSS value", name)
		}
		if len(s) > maxStringLen {
			return fmt.Errorf("colorTokens[%q] is too long", name)
		}
	}
	return nil
}

func validateFontRemap(v any) error {
	arr, ok := v.([]any)
	if !ok {
		return fmt.Errorf("fontRemap must be an array")
	}
	for i, entry := range arr {
		e, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("fontRemap[%d] must be an object", i)
		}
		// family is emitted inside single quotes in the generated @font-face,
		// so a stray quote escapes the string literal; src legitimately
		// contains quotes inside url(...)/format(...) so it keeps the looser
		// check. Same asymmetry as useThemeProvider.ts.
		family, _ := e["family"].(string)
		src, _ := e["src"].(string)
		if strings.TrimSpace(family) == "" || strings.TrimSpace(src) == "" {
			return fmt.Errorf("fontRemap[%d] requires non-empty family and src", i)
		}
		for _, f := range []struct {
			name    string
			val     string
			noQuote bool
		}{
			{"family", family, true},
			{"src", src, false},
			{"weight", stringOr(e["weight"]), true},
			{"style", stringOr(e["style"]), true},
		} {
			if f.val == "" {
				continue
			}
			if unsafeCSSValue.MatchString(f.val) || (f.noQuote && strings.Contains(f.val, "'")) {
				return fmt.Errorf("fontRemap[%d].%s contains characters that are not valid in a CSS value", i, f.name)
			}
			if len(f.val) > 1024 {
				return fmt.Errorf("fontRemap[%d].%s is too long", i, f.name)
			}
		}
	}
	return nil
}

func stringOr(v any) string {
	s, _ := v.(string)
	return s
}

// emailColor accepts the colour notations email clients render reliably. The
// value is written into an inline style="…" attribute of the HTML email, so the
// allow-list is deliberately narrower than colorTokens: no quotes, semicolons or
// angle brackets can pass a hex, rgb()/rgba() or bare-keyword pattern.
var emailColor = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|rgba?\([0-9., %]+\)|[a-zA-Z]{3,20})$`)

// emailAddress is a shape check, not RFC 5322: it rejects anything that could
// break out of the mailto: link or the HTML it is rendered into.
var emailAddress = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)

// validateEmail checks the `email` block notifications-server applies to
// outbound mail. Every field is optional; unknown fields are refused so a typo
// fails the write instead of silently doing nothing.
func validateEmail(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("email must be an object")
	}
	for k, raw := range m {
		switch k {
		case "logoUrl":
			s, ok := raw.(string)
			if !ok {
				return fmt.Errorf("email.logoUrl must be a string")
			}
			// Root-relative like every other asset; notifications-server makes it
			// absolute with its BASE_URL, which email clients require.
			if err := validateAssetURL("email.logoUrl", s); err != nil {
				return err
			}
		case "primaryColor", "headerBgColor", "footerBgColor", "footerLinkColor":
			s, ok := raw.(string)
			if !ok || !emailColor.MatchString(s) {
				return fmt.Errorf("email.%s must be a hex (#0047AB), rgb()/rgba() or named colour", k)
			}
		case "supportEmail":
			s, ok := raw.(string)
			if !ok || (s != "" && (len(s) > maxStringLen || !emailAddress.MatchString(s))) {
				return fmt.Errorf("email.supportEmail must be an email address, or \"\" to hide it")
			}
		case "address":
			s, ok := raw.(string)
			if !ok {
				return fmt.Errorf("email.address must be a string")
			}
			if len(s) > maxStringLen {
				return fmt.Errorf("email.address must be at most %d characters", maxStringLen)
			}
			// Rendered into the HTML footer without escaping.
			if strings.ContainsAny(s, "<>\"'`") {
				return fmt.Errorf("email.address must not contain < > \" ' or `")
			}
		case "copyrightStartYear":
			f, ok := raw.(float64)
			if !ok || f != float64(int(f)) || f < 1990 || f > 2100 {
				return fmt.Errorf("email.copyrightStartYear must be a whole year between 1990 and 2100")
			}
		default:
			return fmt.Errorf("email has unexpected field %q; allowed: address, copyrightStartYear, "+
				"footerBgColor, footerLinkColor, headerBgColor, logoUrl, primaryColor, supportEmail", k)
		}
	}
	return nil
}
