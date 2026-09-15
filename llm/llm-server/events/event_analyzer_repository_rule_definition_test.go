package events

import (
	"regexp"
	"testing"

	"nudgebee/llm/security"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An alert can have one event_rules row per source: the rule synced from the
// provider and an empty stub created when the alert first arrived by webhook.
// The lookup must rank them so the populated definition wins, not whichever
// row the database happens to return first.
func TestGetEventRuleDefinitionPrefersPopulatedRow(t *testing.T) {
	ctx := security.NewRequestContextForSuperAdmin()

	assert.Contains(t, eventRuleDefinitionQuery, "order by (er.expr <> '') desc, length(er.expr) desc, er.updated_at desc",
		"rows must be ranked populated-first; unordered, an empty webhook stub can be returned")
	assert.Contains(t, eventRuleDefinitionQuery, "limit 1")

	t.Run("returns the ranked row's definition and annotations", func(t *testing.T) {
		repo, mock := newClaimTestRepo(t)
		mock.ExpectQuery(regexp.QuoteMeta(eventRuleDefinitionQuery)).
			WithArgs("acct-1", "orders-app-health-down").
			WillReturnRows(sqlmock.NewRows([]string{"expr", "annotations"}).
				AddRow(`{"AlarmName":"orders-app-health-down"}`, []byte(`{"summary":"orders app health"}`)))

		expr, annotations, err := repo.GetEventRuleDefinition(ctx, "acct-1", "orders-app-health-down")
		require.NoError(t, err)
		assert.Equal(t, `{"AlarmName":"orders-app-health-down"}`, expr)
		assert.Equal(t, "orders app health", annotations["summary"])
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("no rule for the alert returns empty without error", func(t *testing.T) {
		repo, mock := newClaimTestRepo(t)
		mock.ExpectQuery(regexp.QuoteMeta(eventRuleDefinitionQuery)).
			WithArgs("acct-1", "unknown").
			WillReturnRows(sqlmock.NewRows([]string{"expr", "annotations"}))

		expr, annotations, err := repo.GetEventRuleDefinition(ctx, "acct-1", "unknown")
		require.NoError(t, err)
		assert.Empty(t, expr)
		assert.Nil(t, annotations)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
