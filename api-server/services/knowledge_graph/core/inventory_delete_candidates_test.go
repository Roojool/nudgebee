package core

// DB-gated tests for the delete-candidate queries and the name/id lookups used by
// per-resource deletes and creates. Skips cleanly when no Postgres is reachable.

import (
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"nudgebee/services/internal/database"
	"nudgebee/services/internal/testenv"
)

const (
	candidateTenant  = "b2ca6e00-0000-4000-8000-000000000008"
	candidateAccount = "b2ca6e00-0000-4000-8000-00000000ffff"
)

type candidateFixture struct {
	dbm  *database.DatabaseManager
	svc  *Service
	repo *ResourcePatchRepository
}

func newCandidateFixture(t *testing.T) *candidateFixture {
	t.Helper()
	dbm := testenv.RequireMetastore(t)
	cleanup := func() {
		_, _ = dbm.Exec(`DELETE FROM knowledge_graph_node WHERE tenant_id = $1::uuid`, candidateTenant)
		_, _ = dbm.Exec(`DELETE FROM cloud_resourses WHERE account = $1::uuid`, candidateAccount)
		_, _ = dbm.Exec(`DELETE FROM k8s_workloads WHERE tenant_id = $1::uuid`, candidateTenant)
		_, _ = dbm.Exec(`DELETE FROM k8s_nodes WHERE tenant_id = $1::uuid`, candidateTenant)
	}
	cleanup()
	t.Cleanup(cleanup)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &candidateFixture{dbm: dbm, svc: NewService(newTestRequestContext(), logger, dbm), repo: NewResourcePatchRepository(dbm, logger)}
}

// seedInventory inserts inventory rows with foreign-key triggers off: the rows
// reference accounts, tenants and users the test does not create. Needs a role
// allowed to set session_replication_role; skips otherwise.
func (f *candidateFixture) seedInventory(t *testing.T, statements ...string) {
	t.Helper()
	tx, err := f.dbm.BeginTx()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`SET LOCAL session_replication_role = replica`); err != nil {
		t.Skipf("cannot seed inventory without foreign keys: %v", err)
	}
	for _, stmt := range statements {
		if _, err := tx.Exec(stmt, candidateTenant, candidateAccount); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func (f *candidateFixture) saveNode(t *testing.T, source string, nodeType NodeType, specificType, name string, props map[string]interface{}) *DbNode {
	t.Helper()
	if props == nil {
		props = map[string]interface{}{}
	}
	props["name"] = name
	props["specific_type"] = specificType
	key := BuildUniqueKey(source, candidateAccount, "", nodeType, "", name)
	node := NewNode(nodeType, key, props, candidateTenant, candidateAccount, source)
	if err := f.svc.SaveNodes([]*DbNode{node}, 1); err != nil {
		t.Fatal(err)
	}
	return node
}

func sorted(values []string) string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestCloudDeleteCandidates(t *testing.T) {
	f := newCandidateFixture(t)
	const (
		liveRow    = "c0000000-0000-4000-8000-000000000001"
		deletedRow = "c0000000-0000-4000-8000-000000000002"
		k8sRow     = "c0000000-0000-4000-8000-000000000003"
		noRow      = "c0000000-0000-4000-8000-000000000004"
	)
	f.seedInventory(t,
		`INSERT INTO cloud_resourses (id, account, tenant, cloud_provider, region, resourse_id, name, type, is_active, status)
		 VALUES ('`+liveRow+`', $2, $1, 'AWS', 'us-east-1', 'i-live', 'live', 'compute-instance', true, 'Active')`,
		`INSERT INTO cloud_resourses (id, account, tenant, cloud_provider, region, resourse_id, name, type, is_active, status)
		 VALUES ('`+deletedRow+`', $2, $1, 'AWS', 'us-east-1', 'i-gone', 'gone', 'compute-instance', false, 'Deleted')`,
		`INSERT INTO cloud_resourses (id, account, tenant, cloud_provider, region, resourse_id, name, type, is_active, status)
		 VALUES ('`+k8sRow+`', $2, $1, 'K8s', 'us-east-1', 'wl', 'wl', 'Deployment', false, 'Inactive')`,
	)
	f.saveNode(t, "aws", NodeTypeComputeInstance, "EC2Instance", "live", map[string]interface{}{"nb_resource_id": liveRow})
	f.saveNode(t, "aws", NodeTypeComputeInstance, "EC2Instance", "gone", map[string]interface{}{"nb_resource_id": deletedRow})
	f.saveNode(t, "k8s", NodeTypeWorkload, "KubernetesDeployment", "wl", map[string]interface{}{"nb_resource_id": k8sRow})
	f.saveNode(t, "aws", NodeTypeVPC, "VPC", "inferred-no-row", map[string]interface{}{"nb_resource_id": noRow})
	f.saveNode(t, "aws", NodeTypeCloudResource, "CloudResource", "bad-id", map[string]interface{}{"nb_resource_id": "not-a-uuid"})

	got, err := f.repo.CloudDeleteCandidates(candidateTenant, candidateAccount, []string{"aws", "gcp", "azure"}, 10)
	if err != nil {
		t.Fatalf("query (a non-uuid nb_resource_id must not break it): %v", err)
	}
	if sorted(got) != deletedRow {
		t.Errorf("candidates %v, want only the deleted row's node (not the live one, the k8s one, the row-less inferred one)", got)
	}
}

func TestK8sWorkloadDeleteCandidates(t *testing.T) {
	f := newCandidateFixture(t)
	const (
		liveWorkload     = "d0000000-0000-4000-8000-000000000001"
		inactiveWorkload = "d0000000-0000-4000-8000-000000000002"
		removedWorkload  = "d0000000-0000-4000-8000-000000000003"
		deadPod          = "d0000000-0000-4000-8000-000000000004"
	)
	insert := func(id, kind string, active bool) string {
		activeSQL := "false"
		if active {
			activeSQL = "true"
		}
		return `INSERT INTO k8s_workloads (tenant_id, cloud_account_id, cloud_resource_id, external_id, namespace, name, kind,
			is_active, total_pods, ready_pods, last_seen, labels, meta)
			VALUES ($1, $2, '` + id + `', '` + id + `', 'shop', '` + id + `', '` + kind + `', ` + activeSQL + `, 1, 1, now(), '{}', '{}')`
	}
	f.seedInventory(t, insert(liveWorkload, "Deployment", true), insert(inactiveWorkload, "Deployment", false), insert(deadPod, "Pod", false))
	f.saveNode(t, "k8s", NodeTypeWorkload, "KubernetesDeployment", "live", map[string]interface{}{"nb_resource_id": liveWorkload})
	f.saveNode(t, "k8s", NodeTypeWorkload, "KubernetesDeployment", "inactive", map[string]interface{}{"nb_resource_id": inactiveWorkload})
	f.saveNode(t, "k8s", NodeTypeWorkload, "KubernetesDeployment", "removed", map[string]interface{}{"nb_resource_id": removedWorkload})
	f.saveNode(t, "k8s", NodeTypePod, "KubernetesPod", "dead-pod", map[string]interface{}{"nb_resource_id": deadPod})

	got, err := f.repo.K8sWorkloadDeleteCandidates(candidateTenant, candidateAccount, []string{"KubernetesDeployment", "KubernetesService"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if sorted(got) != sorted([]string{inactiveWorkload, removedWorkload}) {
		t.Errorf("candidates %v, want the inactive and the removed deployment only (pods are not a detected kind)", got)
	}
}

func TestK8sNodeDeleteCandidatesAndNameLookup(t *testing.T) {
	f := newCandidateFixture(t)
	nodeInsert := func(id, name string, active bool) string {
		activeSQL := "false"
		if active {
			activeSQL = "true"
		}
		return `INSERT INTO k8s_nodes (tenant_id, cloud_account_id, cloud_resource_id, name, is_active,
			memory_capacity, cpu_capacity, memory_allocatable, cpu_allocatable)
			VALUES ($1, $2, '` + id + `', '` + name + `', ` + activeSQL + `, 1, 1, 1, 1)`
	}
	f.seedInventory(t,
		nodeInsert("e0000000-0000-4000-8000-000000000001", "node-live", true),
		nodeInsert("e0000000-0000-4000-8000-000000000002", "node-gone", false),
		// Replaced under the same name: an inactive and an active row.
		nodeInsert("e0000000-0000-4000-8000-000000000003", "node-replaced", false),
		nodeInsert("e0000000-0000-4000-8000-000000000004", "node-replaced", true),
	)
	built := map[string]interface{}{"node_creation_time": "2026-09-17T08:00:00Z"}
	f.saveNode(t, "k8s", NodeTypeNode, "KubernetesNode", "node-live", map[string]interface{}{"node_creation_time": built["node_creation_time"]})
	gone := f.saveNode(t, "k8s", NodeTypeNode, "KubernetesNode", "node-gone", map[string]interface{}{"node_creation_time": built["node_creation_time"]})
	f.saveNode(t, "k8s", NodeTypeNode, "KubernetesNode", "node-replaced", map[string]interface{}{"node_creation_time": built["node_creation_time"]})
	f.saveNode(t, "k8s", NodeTypeNode, "KubernetesNode", "pod-placeholder", nil) // synthesized for a pod, never had a row
	f.saveNode(t, "traces", NodeTypeNode, "KubernetesNode", "node-gone", nil)    // another source's node of the same name

	names, err := f.repo.K8sNodeDeleteCandidates(candidateTenant, candidateAccount, "KubernetesNode", 10)
	if err != nil {
		t.Fatal(err)
	}
	if sorted(names) != "node-gone" {
		t.Errorf("candidates %v, want only node-gone", names)
	}

	matches, err := f.repo.FindActiveNodesByName(candidateTenant, candidateAccount, "k8s", "KubernetesNode", "node-gone")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != gone.ID {
		t.Errorf("name lookup returned %d nodes, want the k8s node only", len(matches))
	}

	byID, err := f.repo.FindActiveNodeByID(candidateTenant, gone.ID)
	if err != nil || byID == nil || byID.SpecificType != "KubernetesNode" {
		t.Errorf("id lookup = %+v, %v", byID, err)
	}
	missing, err := f.repo.FindActiveNodeByID(candidateTenant, "f0000000-0000-4000-8000-000000000000")
	if err != nil || missing != nil {
		t.Errorf("id lookup of an unknown id = %+v, %v; want nil, nil", missing, err)
	}
}
