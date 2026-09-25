package core

import (
	"context"
	"log/slog"
	"testing"

	"nudgebee/llm/security"
	toolcore "nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
)

func matchCtx(t *testing.T) *security.RequestContext {
	t.Helper()
	return security.NewRequestContext(context.Background(),
		security.NewSecurityContextForTenantAccountAdmin("t", "u", nil),
		slog.Default(), otel.Tracer("t"), otel.Meter("m"))
}

func tcfg(name, host string) toolcore.ToolConfig {
	return toolcore.ToolConfig{Name: name, Values: []toolcore.ToolConfigValue{{Name: "host", Value: host}}}
}

// The resource an alert is about is the one signal that says which system the
// question concerns. Matching it lives here rather than in each tool: every
// tool with configured targets has the same question, and only the argument
// parsing is tool-specific.
func TestMatchConfigBySubject(t *testing.T) {
	ctx := matchCtx(t)
	configs := []toolcore.ToolConfig{
		tcfg("orders-primary", "orders-db-primary,orders-db-.*-primary"),
		tcfg("orders-replica", "orders-db-replica"),
		tcfg("reporting", ""),
	}

	t.Run("matches a config by name", func(t *testing.T) {
		got, ok := matchConfigBySubject(ctx, "reporting", configs)
		assert.True(t, ok)
		assert.Equal(t, "reporting", got.Name)
	})

	t.Run("matches a host pattern", func(t *testing.T) {
		got, ok := matchConfigBySubject(ctx, "orders-db-eu-primary", configs)
		assert.True(t, ok)
		assert.Equal(t, "orders-primary", got.Name)
	})

	// A database running in a pod has no configured integration at all. No match
	// is the correct answer and more useful than picking one of them.
	t.Run("a subject that is none of them does not match", func(t *testing.T) {
		_, ok := matchConfigBySubject(ctx, "data-postgresql-0", configs)
		assert.False(t, ok)
	})

	t.Run("no subject never matches", func(t *testing.T) {
		_, ok := matchConfigBySubject(ctx, "", configs)
		assert.False(t, ok)
	})

	t.Run("an invalid host pattern is skipped, not fatal", func(t *testing.T) {
		_, ok := matchConfigBySubject(ctx, "anything", []toolcore.ToolConfig{tcfg("broken", "[unclosed")})
		assert.False(t, ok)
	})
}

func TestSubjectOfRequest(t *testing.T) {
	assert.Equal(t, "`nudgebee-oss/data-postgresql-0`", subjectOfRequest(toolcore.NbToolContext{
		QueryConfig: toolcore.NBQueryConfig{Namespace: "nudgebee-oss", Workload: "data-postgresql-0"},
	}))
	assert.Equal(t, "`beehive`", subjectOfRequest(toolcore.NbToolContext{
		QueryConfig: toolcore.NBQueryConfig{Workload: "beehive"},
	}))
	assert.Empty(t, subjectOfRequest(toolcore.NbToolContext{}), "a free-form chat supplies no subject")
}
