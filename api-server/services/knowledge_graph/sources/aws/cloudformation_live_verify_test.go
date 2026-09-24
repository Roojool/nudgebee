package aws

import (
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"nudgebee/services/config"
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"

	_ "github.com/lib/pq"
)

// TestVerifyStackResourcesFromRow_AgainstLiveDB is an offline verification
// harness, NOT a CI unit test. It reads the *real* stack rows the collector
// wrote into cloud_resourses and runs the actual reader over them, which is the
// one thing the unit tests cannot prove: that a payload produced by the writer in
// the other Go module, round-tripped through jsonb, decodes here.
//
// Read-only. It opens no write transaction and builds no graph, so it cannot
// tombstone anything on a shared environment.
//
// Skipped unless KG_VERIFY_DB_URL is set, so `make test` / CI never touch a DB:
//
//	KG_VERIFY_DB_URL='postgresql://postgres:...@localhost:5432/nudgebee?sslmode=disable' \
//	KG_VERIFY_ACCOUNT='19707e32-c54e-47de-8003-9c253a4428da' \
//	go test ./knowledge_graph/sources/aws/ -run TestVerifyStackResourcesFromRow_AgainstLiveDB -v
func TestVerifyStackResourcesFromRow_AgainstLiveDB(t *testing.T) {
	dbURL := os.Getenv("KG_VERIFY_DB_URL")
	if dbURL == "" {
		t.Skip("KG_VERIFY_DB_URL not set — skipping live-DB verification harness")
	}
	accountID := os.Getenv("KG_VERIFY_ACCOUNT")
	if accountID == "" {
		t.Skip("KG_VERIFY_ACCOUNT not set — skipping live-DB verification harness")
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	const q = `
		SELECT resourse_id, name, COALESCE(region,''), COALESCE(meta::text,'{}')
		FROM public.cloud_resourses
		WHERE is_active AND type = 'stack' AND account = $1`
	rows, err := db.Query(q, accountID)
	if err != nil {
		t.Fatalf("query stacks: %v", err)
	}
	defer func() { _ = rows.Close() }()

	src, err := NewAWSSource(AWSSourceConfig{}, nil)
	if err != nil {
		t.Fatalf("NewAWSSource: %v", err)
	}
	byID := map[string]sources.CloudResourceRow{}
	nodes := []*core.DbNode{}
	for rows.Next() {
		var resourceID, name, region, meta string
		if err := rows.Scan(&resourceID, &name, &region, &meta); err != nil {
			t.Fatalf("scan: %v", err)
		}
		byID[resourceID] = sources.CloudResourceRow{
			ResourceID: resourceID, Name: name, Type: "stack",
			Region: region, Meta: json.RawMessage(meta),
		}
		nodes = append(nodes, &core.DbNode{
			NodeType: core.NodeTypeInfraStack,
			Properties: map[string]interface{}{
				"name": name, "resource_id": resourceID, "region": region,
			},
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatalf("no active stack rows for account %s — nothing to verify", accountID)
	}
	src.metaByTypeAndID = map[string]map[string]sources.CloudResourceRow{"stack": byID}

	prev := config.Config.FeatureKGCFNStackResourcesFromDB
	config.Config.FeatureKGCFNStackResourcesFromDB = true
	defer func() { config.Config.FeatureKGCFNStackResourcesFromDB = prev }()

	hits, misses, totalResources, usable, retired := 0, 0, 0, 0, 0
	for _, node := range nodes {
		summaries, ok := src.stackResourcesFromRow(node)
		if !ok {
			misses++
			t.Logf("MISS (would fall back to the CLI): %s", node.Properties["name"])
			continue
		}
		hits++
		totalResources += len(summaries)
		for _, s := range summaries {
			// Mirror createCloudFormationEdges: retired resources are skipped
			// before any lookup. AWS genuinely returns a blank PhysicalResourceId
			// for DELETE_COMPLETE entries, so asserting on them would fail on
			// real data while proving nothing.
			if strings.HasPrefix(s.ResourceStatus, "DELETE") || strings.HasSuffix(s.ResourceStatus, "FAILED") {
				retired++
				continue
			}
			// For everything that does reach the lookup, a blank
			// PhysicalResourceId is the silent failure that matters: the lookup
			// would miss and the stack would contribute no edges.
			if s.PhysicalResourceId == "" || s.ResourceType == "" || s.LogicalResourceId == "" {
				t.Errorf("incomplete live summary on stack %s: %+v", node.Properties["name"], s)
			}
			usable++
		}
	}

	t.Logf("live stacks=%d served_from_row=%d would_use_cli=%d resources=%d (usable=%d retired=%d)",
		len(nodes), hits, misses, totalResources, usable, retired)
	if hits == 0 {
		t.Fatalf("no stack was served from its row — the collector has not captured this account yet, or the reader is broken")
	}
}
