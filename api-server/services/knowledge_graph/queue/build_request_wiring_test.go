package queue

// The wiring test: a KG update message names a cloud account, and the build that
// eventually runs must be account-scoped. This covers the seam between the queue
// message and core.BuildRequest — the part the sweep and context tests below it
// assume is correct. Everything here is in-process; RabbitMQ is not involved.

import (
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"nudgebee/services/knowledge_graph/core"
	kgmodels "nudgebee/services/knowledge_graph/models"
)

func testFilter(accountIDs, sources, flowSources []string) *kgmodels.KnowledgeGraphTenantFilter {
	return &kgmodels.KnowledgeGraphTenantFilter{
		ID:          uuid.New(),
		TenantID:    uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		FilterName:  "default",
		AccountIDs:  accountIDs,
		Sources:     sources,
		FlowSources: flowSources,
		Enabled:     true,
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBuildRequestForFilter(t *testing.T) {
	tests := []struct {
		name            string
		filter          *kgmodels.KnowledgeGraphTenantFilter
		claim           *core.BuildClaim
		wantNil         bool
		wantPartial     bool
		wantAccountIDs  []string
		wantSources     []string
		wantFlowSources []string
	}{
		{
			name:            "tenant-wide claim reproduces the historical build",
			filter:          testFilter([]string{"acct-a", "acct-b"}, []string{"aws", "k8s"}, []string{"ebpf"}),
			claim:           &core.BuildClaim{},
			wantPartial:     false,
			wantAccountIDs:  []string{"acct-a", "acct-b"},
			wantSources:     []string{"aws", "k8s"},
			wantFlowSources: []string{"ebpf"},
		},
		{
			name:            "account-scoped claim narrows to that account and drops flow sources",
			filter:          testFilter([]string{"acct-a", "acct-b"}, []string{"aws", "k8s"}, []string{"ebpf"}),
			claim:           &core.BuildClaim{AccountIDs: []string{"acct-b"}},
			wantPartial:     true,
			wantAccountIDs:  []string{"acct-b"},
			wantSources:     []string{"aws", "k8s"},
			wantFlowSources: nil,
		},
		{
			name:   "explicitly requested flow sources are honoured on a scoped build",
			filter: testFilter([]string{"acct-a"}, []string{"aws"}, []string{"ebpf"}),
			claim: &core.BuildClaim{
				AccountIDs:  []string{"acct-a"},
				FlowSources: []string{"traces"},
			},
			wantPartial:     true,
			wantAccountIDs:  []string{"acct-a"},
			wantSources:     []string{"aws"},
			wantFlowSources: []string{"traces"},
		},
		{
			name:    "an account this filter does not cover is skipped, not built",
			filter:  testFilter([]string{"acct-a"}, []string{"aws"}, nil),
			claim:   &core.BuildClaim{AccountIDs: []string{"acct-zzz"}},
			wantNil: true,
		},
		{
			name:    "a source this filter excludes is skipped, never widened",
			filter:  testFilter([]string{"acct-a"}, []string{"aws"}, nil),
			claim:   &core.BuildClaim{AccountIDs: []string{"acct-a"}, Sources: []string{"gcp"}},
			wantNil: true,
		},
		{
			name:           "a filter with no account restriction accepts the requested account",
			filter:         testFilter(nil, []string{"aws"}, nil),
			claim:          &core.BuildClaim{AccountIDs: []string{"acct-new"}},
			wantPartial:    true,
			wantAccountIDs: []string{"acct-new"},
			wantSources:    []string{"aws"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildRequestForFilter(tc.filter, tc.claim, discardLogger())

			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected the filter to be skipped, got a build for accounts %v sources %v",
						got.AccountIDs, got.Sources)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a build request, got nil (filter skipped)")
			}
			if got.Partial != tc.wantPartial {
				t.Errorf("Partial = %v, want %v", got.Partial, tc.wantPartial)
			}
			if !reflect.DeepEqual(got.AccountIDs, tc.wantAccountIDs) {
				t.Errorf("AccountIDs = %v, want %v", got.AccountIDs, tc.wantAccountIDs)
			}
			if !reflect.DeepEqual(got.Sources, tc.wantSources) {
				t.Errorf("Sources = %v, want %v", got.Sources, tc.wantSources)
			}
			if !reflect.DeepEqual(got.FlowSources, tc.wantFlowSources) {
				t.Errorf("FlowSources = %v, want %v", got.FlowSources, tc.wantFlowSources)
			}
			if !got.SaveToDB {
				t.Error("SaveToDB = false; the queue path must persist")
			}
			// Partial builds must always carry a scope — BuildGraphs rejects one
			// without accounts, which would fail every build for this tenant.
			if got.Partial && len(got.AccountIDs) == 0 {
				t.Error("partial build request carries no account ids")
			}
		})
	}
}

// End-to-end scope wiring: the account id on the wire must survive enqueue,
// claim-coalescing and filter intersection, and arrive as a partial BuildRequest.
func TestMessageScopeReachesBuildRequest(t *testing.T) {
	msg := KGUpdateMessage{
		TenantID:   "11111111-1111-4111-8111-111111111111",
		Source:     "cloud-collector",
		AccountIDs: []string{"acct-b"},
	}

	// What the repository reconstructs from the queued row.
	claim := &core.BuildClaim{TenantID: msg.TenantID, AccountIDs: msg.AccountIDs}

	filter := testFilter([]string{"acct-a", "acct-b"}, []string{"aws", "k8s"}, []string{"ebpf"})
	req := buildRequestForFilter(filter, claim, discardLogger())

	if req == nil {
		t.Fatal("an account-scoped message produced no build")
	}
	if !req.Partial {
		t.Error("an account-scoped message must produce a partial build")
	}
	if len(req.AccountIDs) != 1 || req.AccountIDs[0] != "acct-b" {
		t.Errorf("build accounts = %v, want only the account named on the message", req.AccountIDs)
	}
	if len(req.FlowSources) != 0 {
		t.Errorf("FlowSources = %v, want none for an account-scoped rebuild", req.FlowSources)
	}
}
