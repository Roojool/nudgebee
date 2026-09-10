package event

import (
	"log/slog"
	"nudgebee/services/internal/database"
	"nudgebee/services/security"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	cmTenant  = "11111111-1111-1111-1111-111111111111"
	cmAccount = "22222222-2222-2222-2222-222222222222"
	cmCrid    = "33333333-3333-3333-3333-333333333333"
)

func cmTestCtx() *security.RequestContext {
	return security.NewRequestContextForTenantAdmin(cmTenant, slog.Default(), nil, nil)
}

func configMapEvent() *Event {
	return &Event{
		AccountId:        cmAccount,
		Tenant:           cmTenant,
		SubjectType:      "configmap",
		SubjectName:      "sessions-config",
		SubjectNamespace: "sessions",
		AggregationKey:   "ConfigurationChange/KubernetesResource/Change",
		FindingType:      "configuration_change",
	}
}

func consumerRows(pairs ...[2]string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"name", "kind"})
	for _, p := range pairs {
		rows = rows.AddRow(p[0], p[1])
	}
	return rows
}

// A ConfigMap consumed by exactly one workload is attributed to it: the change
// carries that workload's resource link and owner, so it reaches the workload's
// change history instead of sitting in the event list as an orphan.
func TestLinkK8sCloudResourceId_ConfigMapWithOneConsumer(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`USES_CONFIG`).
		WithArgs(cmTenant, cmAccount, "sessions", "sessions-config").
		WillReturnRows(consumerRows([2]string{"sessions-api", "Deployment"}))
	mock.ExpectQuery(`FROM k8s_workloads`).
		WithArgs(cmTenant, cmAccount, "sessions", "sessions-api").
		WillReturnRows(sqlmock.NewRows([]string{"cloud_resource_id"}).AddRow(cmCrid))

	ev := configMapEvent()
	linkK8sCloudResourceId(cmTestCtx(), &database.DatabaseManager{Db: sqlx.NewDb(db, "postgres")}, cmTenant, ev)

	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, cmCrid, ev.CloudResourceId)
	assert.Equal(t, "sessions-api", ev.SubjectOwner)
	assert.Equal(t, "Deployment", ev.SubjectOwnerKind)
	assert.Equal(t, "Deployment/sessions-api", ev.Labels[ConfigMapConsumersLabel])
}

// With several consumers, any single resource link would be arbitrary — but the
// full set still has to reach the event, because "which application just had
// its configuration changed" is the only reason the event is interesting.
func TestLinkK8sCloudResourceId_ConfigMapWithSeveralConsumers(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`USES_CONFIG`).
		WillReturnRows(consumerRows(
			[2]string{"sessions-api", "Deployment"},
			[2]string{"sessions-worker", "Deployment"},
		))

	ev := configMapEvent()
	linkK8sCloudResourceId(cmTestCtx(), &database.DatabaseManager{Db: sqlx.NewDb(db, "postgres")}, cmTenant, ev)

	require.NoError(t, mock.ExpectationsWereMet())
	assert.Empty(t, ev.CloudResourceId, "must not pick one of several consumers")
	assert.Empty(t, ev.SubjectOwner)
	assert.Equal(t, "Deployment/sessions-api,Deployment/sessions-worker", ev.Labels[ConfigMapConsumersLabel])
}

// The regression this guards: the pre-existing default branch treated any
// non-pod subject as a workload name and matched it against k8s_workloads by
// (namespace, name) with no kind filter. Charts name a Deployment and its
// config the same thing constantly, so an edit of `grafana` the ConfigMap
// would link to `grafana` the Deployment whether or not it reads it — a causal
// claim invented from a naming coincidence.
func TestLinkK8sCloudResourceId_ConfigMapNeverNameMatchesAWorkload(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// No USES_CONFIG edge exists (graph not built yet, or nothing reads it).
	mock.ExpectQuery(`USES_CONFIG`).WillReturnRows(consumerRows())
	// Any k8s_workloads lookup after that would be the name match. None is
	// registered, so sqlmock fails the test if one is attempted.

	ev := configMapEvent()
	ev.SubjectName = "grafana" // same name as a Deployment in this namespace
	linkK8sCloudResourceId(cmTestCtx(), &database.DatabaseManager{Db: sqlx.NewDb(db, "postgres")}, cmTenant, ev)

	require.NoError(t, mock.ExpectationsWereMet())
	assert.Empty(t, ev.CloudResourceId, "unattributed beats wrongly attributed")
	assert.Empty(t, ev.Labels[ConfigMapConsumersLabel])
}

// Workload subjects keep resolving by name — the ConfigMap branch must not
// have changed the path every other change event takes.
func TestLinkK8sCloudResourceId_WorkloadSubjectUnchanged(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`FROM k8s_workloads`).
		WithArgs(cmTenant, cmAccount, "sessions", "sessions-api").
		WillReturnRows(sqlmock.NewRows([]string{"cloud_resource_id"}).AddRow(cmCrid))

	ev := configMapEvent()
	ev.SubjectType = "deployment"
	ev.SubjectName = "sessions-api"
	linkK8sCloudResourceId(cmTestCtx(), &database.DatabaseManager{Db: sqlx.NewDb(db, "postgres")}, cmTenant, ev)

	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, cmCrid, ev.CloudResourceId)
}

// A ConfigMap read by hundreds of workloads (a shared CA bundle) must not write
// an unbounded string into every event row.
func TestLinkK8sCloudResourceId_ConsumerLabelIsBounded(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{"name", "kind"})
	for i := 0; i < maxConfigMapConsumersLabelled+7; i++ {
		rows = rows.AddRow("workload", "Deployment")
	}
	mock.ExpectQuery(`USES_CONFIG`).WillReturnRows(rows)

	ev := configMapEvent()
	linkK8sCloudResourceId(cmTestCtx(), &database.DatabaseManager{Db: sqlx.NewDb(db, "postgres")}, cmTenant, ev)

	require.NoError(t, mock.ExpectationsWereMet())
	label := ev.Labels[ConfigMapConsumersLabel]
	assert.Contains(t, label, "+7 more", "the count that was dropped has to be stated")
	assert.Equal(t, maxConfigMapConsumersLabelled, len(splitConsumers(label)))
}

// splitConsumers counts the entries in the label, ignoring the "+N more" tail.
func splitConsumers(label string) []string {
	if label == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(label, ",") {
		if part != "" && part[0] != '+' {
			out = append(out, part)
		}
	}
	return out
}
