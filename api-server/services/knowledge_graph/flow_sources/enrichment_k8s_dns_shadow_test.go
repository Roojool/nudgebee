package flow_sources

import (
	"testing"

	"nudgebee/services/knowledge_graph/core"
)

const (
	shadowTestAccount      = "acct-k8s-1"
	shadowTestOtherAccount = "acct-k8s-2"
)

func k8sSvc(id, name, namespace, account string) *core.DbNode {
	n := makeNode(id, core.NodeTypeK8sService, map[string]interface{}{
		"name": name, "namespace": namespace,
	})
	n.CloudAccountID = account
	n.UniqueKey = account + ":" + namespace + "/" + name
	return n
}

func shadowCtx(nodes ...*core.DbNode) *MatchingContext {
	return &MatchingContext{
		CloudAccountID: shadowTestAccount,
		NodeMatcher:    NewNodeMatcher(nodes),
	}
}

// The live ladder must keep answering exactly as it did before shadow mode.
// This is the load-bearing guarantee of PR 1: the stricter rule is measured,
// never enforced, so no edge may appear or disappear because of it.
func TestK8sDNSShadow_LiveBehaviourUnchanged(t *testing.T) {
	// A region-truncated AWS endpoint that the eBPF sensor emitted before
	// 2026-09-19, plus an in-cluster service that shares its leftmost label.
	demoKafka := k8sSvc("demo-kafka", "kafka", "demo", shadowTestAccount)

	got := NewK8sInternalDNSStrategy().Match("kafka.us-west", shadowCtx(demoKafka))

	if !got.Matched {
		t.Fatal("live behaviour changed: kafka.us-west no longer matches; shadow mode must not alter matching")
	}
	if got.Node != demoKafka {
		t.Errorf("live behaviour changed: matched %v, want demo/kafka", got.Node.UniqueKey)
	}
	if got.MatchedBy != "k8s_name:kafka" {
		t.Errorf("MatchedBy = %q, want %q (the name-only fallback)", got.MatchedBy, "k8s_name:kafka")
	}
}

// Each class of disagreement must carry its own reason code — a row labelled
// with a near-neighbour's code sends a reviewer looking for something that was
// never there.
func TestK8sDNSShadow_ReasonCodes(t *testing.T) {
	tests := []struct {
		name       string
		hostname   string
		nodes      []*core.DbNode
		wantReason string
		wantStrict string // unique key the strict rule would pick; "" = it declines
	}{
		{
			name:       "truncated AWS endpoint hijacked by name-only",
			hostname:   "kafka.us-west",
			nodes:      []*core.DbNode{k8sSvc("a", "kafka", "demo", shadowTestAccount)},
			wantReason: ShadowReasonAWSEndpointShaped,
		},
		{
			name:       "truncated AWS endpoint whose label collides with a service",
			hostname:   "email.us-east",
			nodes:      []*core.DbNode{k8sSvc("b", "email", "demo", shadowTestAccount)},
			wantReason: ShadowReasonAWSEndpointShaped,
		},
		{
			name:       "real k8s DNS bound to the wrong namespace",
			hostname:   "postgresql.nudgebee",
			nodes:      []*core.DbNode{k8sSvc("c", "postgresql", "on-prem-test", shadowTestAccount)},
			wantReason: ShadowReasonNamespaceNotFound,
		},
		{
			name:     "name+namespace ambiguous across accounts",
			hostname: "postgresql.nudgebee",
			nodes: []*core.DbNode{
				// No same-account name+namespace hit, so the strict rule widens
				// to any account and finds two equally good candidates.
				k8sSvc("d", "postgresql", "nudgebee", shadowTestOtherAccount),
				k8sSvc("e", "postgresql", "nudgebee", "acct-k8s-3"),
				k8sSvc("f", "postgresql", "elsewhere", shadowTestAccount),
			},
			wantReason: ShadowReasonNamespaceAmbiguous,
		},
		{
			name:     "strict prefers the qualified cross-account node",
			hostname: "postgresql.nudgebee",
			nodes: []*core.DbNode{
				k8sSvc("g", "postgresql", "elsewhere", shadowTestAccount),
				k8sSvc("h", "postgresql", "nudgebee", shadowTestOtherAccount),
			},
			wantReason: ShadowReasonStricterPickedOther,
			wantStrict: shadowTestOtherAccount + ":nudgebee/postgresql",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewK8sInternalDNSStrategy().Match(tt.hostname, shadowCtx(tt.nodes...))

			if !got.Matched {
				t.Fatalf("live rule declined %q; this test needs it to match so a disagreement exists", tt.hostname)
			}
			if got.ShadowDiff == nil {
				t.Fatalf("no ShadowDiff for %q; expected reason %q", tt.hostname, tt.wantReason)
			}
			if got.ShadowDiff.ReasonCode != tt.wantReason {
				t.Errorf("ReasonCode = %q, want %q", got.ShadowDiff.ReasonCode, tt.wantReason)
			}
			if got.ShadowDiff.StrictNodeKey != tt.wantStrict {
				t.Errorf("StrictNodeKey = %q, want %q", got.ShadowDiff.StrictNodeKey, tt.wantStrict)
			}
			if got.ShadowDiff.CurrentNodeKey != got.Node.UniqueKey {
				t.Errorf("CurrentNodeKey = %q, want the node actually returned %q",
					got.ShadowDiff.CurrentNodeKey, got.Node.UniqueKey)
			}
		})
	}
}

// Agreement must be silent. This is what keeps the recorder off the hot path:
// most hostnames reaching this strategy are ordinary external names that
// neither rule matches, and those must not write a review row.
func TestK8sDNSShadow_AgreementProducesNoDiff(t *testing.T) {
	nodes := []*core.DbNode{
		k8sSvc("a", "kafka", "demo", shadowTestAccount),
		k8sSvc("b", "redis-master", "redis", shadowTestAccount),
		k8sSvc("c", "services-server", "nudgebee", shadowTestAccount),
	}

	tests := []struct {
		name      string
		hostname  string
		wantMatch bool
	}{
		{"both match: qualified in-cluster DNS", "kafka.demo", true},
		{"both match: fully qualified", "services-server.nudgebee.svc.cluster.local", true},
		{"both match: short form", "redis-master.redis", true},
		{"both decline: unknown internal name", "nonexistent.nowhere", false},
		{"both decline: unrelated two-label host", "someservice.internal", false},
		// A full AWS hostname never reaches the ladder: LooksLikeK8sServiceName
		// screens on the "amazonaws" substring, so the strategy declines at the
		// top. Only the region-truncated form — which lost that substring — gets
		// far enough to need the strict rule's AWS guard.
		{"both decline: full AWS endpoint screened out earlier", "email.us-east-1.amazonaws.com", false},
		{"both decline: AWS endpoint with no colliding service", "sqs.us-east", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewK8sInternalDNSStrategy().Match(tt.hostname, shadowCtx(nodes...))

			if got.Matched != tt.wantMatch {
				t.Fatalf("Matched = %v, want %v", got.Matched, tt.wantMatch)
			}
			if got.ShadowDiff != nil {
				t.Errorf("rules agreed but a ShadowDiff was recorded: reason=%q current=%q strict=%q",
					got.ShadowDiff.ReasonCode, got.ShadowDiff.CurrentNodeKey, got.ShadowDiff.StrictNodeKey)
			}
		})
	}
}

// Legitimate k8s DNS must not be swept up by the AWS guard inside the strict
// rule — these are the shapes a real cluster emits constantly.
func TestK8sDNSShadow_AWSGuardDoesNotCatchRealK8sDNS(t *testing.T) {
	c := NewAWSClassifier()
	for _, host := range []string{
		"kafka.demo", "kafka.demo.svc.cluster.local", "redis-master.redis",
		"frontend-proxy", "elasticsearch.elasticsearch.svc", "api.default",
		"logs.observability", "config.kube-system", "es.elastic",
		"services-server.nudgebee.svc.cluster.local",
	} {
		if c.IsAWSHostname(host) {
			t.Errorf("AWS guard would reject legitimate k8s DNS %q", host)
		}
	}
}
