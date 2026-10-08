package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	neturl "net/url"
	"nudgebee/services/common"
	"nudgebee/services/integrations/core"
	"nudgebee/services/integrations/signozclient"
	"nudgebee/services/security"
	"strings"
	"time"
)

func init() {
	core.RegisterIntegration(Signoz{})
}

const (
	IntegrationSignoz = "signoz"

	SignozModeSelfHosted = "self_hosted"
	SignozModeCloud      = signozclient.ModeCloud
)

type Signoz struct {
}

func (m Signoz) ValidateConfig(ctx *security.SecurityContext, values []core.IntegrationConfigValue, accountId string) []error {
	configMap := make(map[string]string)
	for _, c := range values {
		configMap[c.Name] = c.Value
	}

	rawURL := strings.TrimSpace(configMap["signoz_url"])
	url := normalizeSignozURL(rawURL)
	username := strings.TrimSpace(configMap["signoz_username"])
	password := configMap["signoz_password"]
	mode := strings.TrimSpace(configMap["signoz_mode"])
	if mode == "" {
		mode = SignozModeSelfHosted
	}

	var errs []error
	if url == "" {
		errs = append(errs, fmt.Errorf("signoz_url is required"))
	} else if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		errs = append(errs, fmt.Errorf("signoz_url must start with http:// or https:// (got %q)", url))
	} else if hasURLPath(rawURL) {
		errs = append(errs, fmt.Errorf("signoz_url must be the base URL only — remove the path after the host (use %q, not %q)", url, rawURL))
	} else if strings.HasSuffix(rawURL, "/") {
		// A trailing slash produces "<url>//api/v2/..." at query time, which
		// SigNoz Cloud answers with a 301; the Go client replays it as a GET and
		// the login endpoint returns HTML, breaking JSON decode. Reject it here
		// so the user stores a clean base URL rather than silently normalizing.
		errs = append(errs, fmt.Errorf("signoz_url must not end with a trailing slash — remove it (use %q, not %q)", strings.TrimRight(rawURL, "/"), rawURL))
	}
	if username == "" {
		errs = append(errs, fmt.Errorf("signoz_username is required"))
	}
	if password == "" {
		errs = append(errs, fmt.Errorf("signoz_password is required"))
	}
	if mode != SignozModeSelfHosted && mode != SignozModeCloud {
		errs = append(errs, fmt.Errorf("signoz_mode must be %q or %q", SignozModeSelfHosted, SignozModeCloud))
	}
	if len(errs) > 0 {
		return errs
	}

	return validateSignoz(url, username, password, mode)
}

// normalizeSignozURL trims whitespace and strips any path/query/fragment so
// users can paste the URL they have open in the browser (e.g.
// https://champion-cub.in2.signoz.cloud/settings/my-settings).
func normalizeSignozURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := neturl.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	return parsed.Scheme + "://" + parsed.Host
}

// validateSignoz logs in exactly the way queries will: signozclient detects
// the login API on self-hosted, so legacy (v1 login) and current (v2
// sessions) SigNoz releases both validate, with no version cap to maintain.
// It bypasses the token cache so a Test click always proves the credentials.
func validateSignoz(url, username, password, mode string) []error {
	tok, err := signozclient.LoginFresh(context.Background(), common.HttpClient(), signozclient.Credentials{
		BaseURL:  url,
		Email:    username,
		Password: password,
		Mode:     mode,
	})
	if err != nil {
		return []error{signozValidationError(url, username, err)}
	}
	slog.Info("signoz: integration validated", "url", url, "mode", mode, "auth", tok.Flavor, "version", signozVersion(url))
	return nil
}

// signozValidationError turns a login failure into a message the user can act
// on. Errors not listed here (unreachable host, ErrNoSupportedAuth) already
// carry the URL and read clearly as-is.
func signozValidationError(url, username string, err error) error {
	var rejected *signozclient.RequestRejectedError
	switch {
	case errors.Is(err, signozclient.ErrInvalidCredentials):
		return fmt.Errorf("invalid SigNoz credentials for %s", username)
	case errors.Is(err, signozclient.ErrUnknownUser):
		return fmt.Errorf("SigNoz at %s has no user %s", url, username)
	case errors.Is(err, signozclient.ErrPasswordAuthDisabled):
		return fmt.Errorf("the SigNoz organization of %s allows SSO login only — use a SigNoz user with email/password login enabled", username)
	case errors.As(err, &rejected):
		return fmt.Errorf("SigNoz rejected the login request: %s", rejected.Message)
	default:
		return err
	}
}

// signozVersion reads GET /api/v1/version (unauthenticated on every SigNoz
// release) for the validation log line only. It never blocks validation, and
// returns "" when the version can't be read.
func signozVersion(url string) string {
	resp, err := common.HttpGet(url+"/api/v1/version", common.HttpWithTimeout(2*time.Second))
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var parsed struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return ""
	}
	return parsed.Version
}

func (m Signoz) Name() string {
	return IntegrationSignoz
}

func (m Signoz) Category() core.IntegrationCategory {
	return core.IntegrationCategoryLog
}

func (m Signoz) ConfigSchema() core.IntegrationSchema {
	return core.IntegrationSchema{
		Type:     core.ToolSchemaTypeObject,
		Required: []string{"signoz_url", "signoz_username", "signoz_password", "signoz_mode"},
		Testable: true,
		Properties: map[string]core.IntegrationSchemaProperty{
			"signoz_mode": {
				Type:        core.ToolSchemaTypeString,
				Description: "SigNoz deployment type. Pick 'cloud' for *.signoz.cloud tenants; 'self_hosted' for your own SigNoz instance (old and current releases are both supported — the login API is detected automatically).",
				Default:     SignozModeSelfHosted,
				Enum:        []any{SignozModeSelfHosted, SignozModeCloud},
				Priority:    90,
				IsTestable:  true,
			},
			"signoz_url": {
				Type:        core.ToolSchemaTypeString,
				Description: "Base URL of the SigNoz instance (e.g., https://signoz.example.com or https://<tenant>.signoz.cloud).",
				Priority:    85,
				IsTestable:  true,
			},
			"signoz_username": {
				Type:        core.ToolSchemaTypeString,
				Description: "Email/username for authenticating with SigNoz.",
				Priority:    72,
				IsTestable:  true,
			},
			"signoz_password": {
				Type:        core.ToolSchemaTypeString,
				Description: "Password for authenticating with SigNoz.",
				IsEncrypted: true,
				Priority:    70,
				IsTestable:  true,
			},
			core.IntegrationConfigName: {
				Type:             core.ToolSchemaTypeString,
				Description:      "Custom name for this SigNoz integration.",
				Default:          "",
				AutoGenerateFunc: "",
				Priority:         100,
			},
			core.AccountId: {
				Type:             core.ToolSchemaTypeArray,
				Description:      "Associated account(s) for this integration.",
				Default:          "",
				AutoGenerateFunc: "listAccounts",
				Priority:         95,
			},
			core.DefaultLogProvider: {
				Type:             core.ToolSchemaTypeBoolean,
				Description:      "Make SigNoz default Log Provider",
				Default:          false,
				AutoGenerateFunc: "",
				Priority:         15,
			},
		},
	}
}
