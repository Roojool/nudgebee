package event

import (
	"nudgebee/services/internal/database"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ingressEvent(name string) *Event {
	return &Event{
		AccountId:        cmAccount,
		Tenant:           cmTenant,
		SubjectType:      "ingress",
		SubjectName:      name,
		SubjectNamespace: "nudgebee-oss",
		AggregationKey:   "ConfigurationChange/KubernetesResource/Change",
		FindingType:      "configuration_change",
	}
}

// The regression this closes has been live: Ingress change events are already
// emitted, and with no ingress branch they fell through to the kindless name
// match against k8s_workloads. In dev, 17 of 25 ingress change events over 90
// days carry a cloud_resource_id assigned that way.
func TestLinkK8sCloudResourceId_IngressResolvesThroughRouting(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`ROUTES_TO_SERVICE`).
		WithArgs(cmTenant, cmAccount, "nudgebee-oss", "app").
		WillReturnRows(relatedRows([2]string{"notifications", "Deployment"}))
	mock.ExpectQuery(`FROM k8s_workloads`).
		WithArgs(cmTenant, cmAccount, "nudgebee-oss", "notifications").
		WillReturnRows(sqlmock.NewRows([]string{"cloud_resource_id"}).AddRow(cmCrid))

	// Ingress `app` routes to service `notifications` — a Deployment named
	// `app` also exists in this namespace, and the name match would have
	// picked it.
	ev := ingressEvent("app")
	linkK8sCloudResourceId(cmTestCtx(), &database.DatabaseManager{Db: sqlx.NewDb(db, "postgres")}, cmTenant, ev)

	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, cmCrid, ev.CloudResourceId)
	assert.Equal(t, "notifications", ev.SubjectOwner)
	assert.Equal(t, "Deployment/notifications", ev.Labels[IngressBackendsLabel])
}

// One Ingress fronting several workloads is the normal case, not an edge
// case: `nudgebee/k8s-collector` fans out to k8s-collector and
// k8s-collector-worker.
func TestLinkK8sCloudResourceId_IngressWithSeveralBackends(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`ROUTES_TO_SERVICE`).
		WillReturnRows(relatedRows(
			[2]string{"k8s-collector", "Deployment"},
			[2]string{"k8s-collector-worker", "Deployment"},
		))

	ev := ingressEvent("k8s-collector")
	linkK8sCloudResourceId(cmTestCtx(), &database.DatabaseManager{Db: sqlx.NewDb(db, "postgres")}, cmTenant, ev)

	require.NoError(t, mock.ExpectationsWereMet())
	assert.Empty(t, ev.CloudResourceId, "must not pick one of several backends")
	assert.Equal(t, "Deployment/k8s-collector,Deployment/k8s-collector-worker", ev.Labels[IngressBackendsLabel])
}

// With no routing edge (graph not built yet, or the Ingress points at
// nothing), the event stays unattributed rather than falling through to the
// name match — unattributed beats wrongly attributed.
func TestLinkK8sCloudResourceId_IngressNeverNameMatchesAWorkload(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`ROUTES_TO_SERVICE`).WillReturnRows(relatedRows())
	// No k8s_workloads expectation is registered: sqlmock fails the test if
	// the name match is attempted.

	ev := ingressEvent("app")
	linkK8sCloudResourceId(cmTestCtx(), &database.DatabaseManager{Db: sqlx.NewDb(db, "postgres")}, cmTenant, ev)

	require.NoError(t, mock.ExpectationsWereMet())
	assert.Empty(t, ev.CloudResourceId)
	assert.Empty(t, ev.Labels[IngressBackendsLabel])
}

// The two traversals must stay distinguishable on the event: a reader
// (or a model) should be able to tell "the config this workload reads
// changed" from "the route into this workload changed".
func TestRelatedWorkloadLabelsAreDistinct(t *testing.T) {
	assert.NotEqual(t, ConfigMapConsumersLabel, IngressBackendsLabel)
}
