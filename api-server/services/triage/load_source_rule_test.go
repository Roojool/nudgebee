package triage

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var sourceRuleColumns = []string{"id", "alert", "source", "expr", "duration", "severity", "category", "alert_type",
	"metric_provider", "metric_provider_source", "external_rule_id", "provider_config", "annotations", "labels", "enabled"}

// GCP suggestions can carry the policy resource name as their alert name while the
// rule is stored under the policy's display name; the policy id finds it.
func TestLoadSourceRule_GCPPolicyIDFallback(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()
	db := sqlx.NewDb(raw, "postgres")
	policy := "projects/p/alertPolicies/11"

	mock.ExpectQuery(regexp.QuoteMeta("WHERE alert = $1 AND account_id = $2 AND tenant_id = $3")).
		WithArgs(policy, "acc", "ten", "GCP_Metric_Alert").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("AND (external_rule_id = $3 OR labels->>'gcp_policy_id' = $3)")).
		WithArgs("acc", "ten", policy).
		WillReturnRows(sqlmock.NewRows(sourceRuleColumns).
			AddRow("r1", "Kubernetes Secret Modified", "GCP_Metric_Alert", "{}", "60s", "critical", "Cloud Monitoring", "metric",
				nil, nil, policy, nil, "{}", "{}", true))

	rule, err := LoadSourceRule(context.Background(), db, "ten", "acc", policy, "GCP_Metric_Alert", policy)
	require.NoError(t, err)
	assert.Equal(t, "r1", rule.ID)
	assert.Equal(t, "Kubernetes Secret Modified", rule.Alert)
	assert.Equal(t, policy, rule.ExternalRuleID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Other keys never take the fallback.
func TestLoadSourceRule_NoFallbackForOtherKeys(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()
	db := sqlx.NewDb(raw, "postgres")

	mock.ExpectQuery(regexp.QuoteMeta("WHERE alert = $1 AND account_id = $2 AND tenant_id = $3")).
		WillReturnError(sql.ErrNoRows)

	_, err = LoadSourceRule(context.Background(), db, "ten", "acc", "HighCPU", "AWS_CloudWatch_Alarm", "arn:aws:cloudwatch:us-east-1:1:alarm:HighCPU")
	assert.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}
