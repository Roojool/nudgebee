package core

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
)

const (
	podCandidateAnyNamespaceQuery = `SELECT DISTINCT ON \(namespace\) workload_name, workload_type, namespace\s+FROM k8s_pods`
	podCandidateInNamespaceQuery  = `(?s)SELECT workload_name, workload_type, namespace\s+FROM k8s_pods.*AND namespace = \$4`
	podCandidateWorkloadQuery     = `FROM k8s_workloads WHERE tenant_id = \$1 AND cloud_account_id = \$2 AND name = \$3 AND namespace = \$4`
)

func podOwnerRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"workload_name", "workload_type", "namespace"})
}

func expectOwnerWorkload(owner, kind, namespace string) {
	pkgMock.ExpectQuery(podCandidateWorkloadQuery).
		WithArgs(testTenant, "acct-1", owner, namespace).
		WillReturnRows(sqlmock.NewRows([]string{"name", "namespace", "kind", "cloud_resource_id"}).
			AddRow(owner, namespace, kind, "res-1"))
}

// A Kibana rule grouped by container and pod delivers "container,pod". Neither
// value says which it is, so the parser offers both; the pod inventory decides.
func TestResolveSubjectFromPodCandidates(t *testing.T) {
	t.Run("a candidate that is a pod makes its workload the subject", func(t *testing.T) {
		sc := ctxForTenant(t, testTenant)
		pkgMock.ExpectQuery(podCandidateAnyNamespaceQuery).
			WithArgs(testTenant, "acct-1", "checkout").
			WillReturnRows(podOwnerRows())
		pkgMock.ExpectQuery(podCandidateAnyNamespaceQuery).
			WithArgs(testTenant, "acct-1", "checkout-54d6f745c5-9j599").
			WillReturnRows(podOwnerRows().AddRow("checkout", "Deployment", "shop"))
		expectOwnerWorkload("checkout", "Deployment", "shop")

		e := &EventIncomingWebhook{PodNameCandidates: []string{"checkout", "checkout-54d6f745c5-9j599"}}
		assert.True(t, ResolveSubjectFromPodCandidates(sc, e, "acct-1"))

		assert.Equal(t, "checkout", e.EventSubjectName)
		assert.Equal(t, "deployment", e.EventSubjectKind)
		assert.Equal(t, "shop", e.EventSubjectNamespace)
		assert.Equal(t, "res-1", e.CloudResourceId)
		assert.Equal(t, "pod_candidate", e.Investigation.Labels["nb_subject_resolution"])
		assert.Equal(t, "true", e.Investigation.Labels["nb_matched_workload"])
		assert.NoError(t, pkgMock.ExpectationsWereMet())
	})

	// "redis-0" exists once per namespace that runs redis. Linking the alert to
	// whichever was seen last would name a workload the alert never mentioned.
	t.Run("a pod name present in two namespaces is a miss", func(t *testing.T) {
		sc := ctxForTenant(t, testTenant)
		pkgMock.ExpectQuery(podCandidateAnyNamespaceQuery).
			WithArgs(testTenant, "acct-1", "redis-0").
			WillReturnRows(podOwnerRows().
				AddRow("redis", "StatefulSet", "cache").
				AddRow("redis", "StatefulSet", "sessions"))

		e := &EventIncomingWebhook{PodNameCandidates: []string{"redis-0"}}
		assert.False(t, ResolveSubjectFromPodCandidates(sc, e, "acct-1"))

		assert.Empty(t, e.EventSubjectName)
		assert.Empty(t, e.EventSubjectNamespace)
		assert.Empty(t, e.Investigation.Labels["pod"])
		assert.NoError(t, pkgMock.ExpectationsWereMet())
	})

	t.Run("a known namespace scopes the lookup", func(t *testing.T) {
		sc := ctxForTenant(t, testTenant)
		pkgMock.ExpectQuery(podCandidateInNamespaceQuery).
			WithArgs(testTenant, "acct-1", "redis-0", "cache").
			WillReturnRows(podOwnerRows().AddRow("redis", "StatefulSet", "cache"))
		expectOwnerWorkload("redis", "StatefulSet", "cache")

		e := &EventIncomingWebhook{
			EventSubjectName:      "redis-0",
			EventSubjectKind:      "pod",
			EventSubjectNamespace: "cache",
			PodNameCandidates:     []string{"redis-0"},
			Investigation:         EventIncomingWebhookInvestigation{Labels: map[string]string{"pod": "redis-0"}},
		}
		assert.True(t, ResolveSubjectFromPodCandidates(sc, e, "acct-1"))

		assert.Equal(t, "redis", e.EventSubjectName)
		assert.Equal(t, "statefulset", e.EventSubjectKind)
		assert.Equal(t, "cache", e.EventSubjectNamespace)
		assert.NoError(t, pkgMock.ExpectationsWereMet())
	})

	t.Run("no candidates or no account skips the lookup", func(t *testing.T) {
		sc := ctxForTenant(t, testTenant)
		assert.False(t, ResolveSubjectFromPodCandidates(sc, nil, "acct-1"))
		assert.False(t, ResolveSubjectFromPodCandidates(sc, &EventIncomingWebhook{EventSubjectName: "checkout"}, "acct-1"))
		assert.False(t, ResolveSubjectFromPodCandidates(sc, &EventIncomingWebhook{PodNameCandidates: []string{"checkout-0"}}, ""))
	})
}
