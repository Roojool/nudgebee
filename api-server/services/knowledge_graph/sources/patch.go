package sources

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"nudgebee/services/knowledge_graph/core"
)

// Per-resource update contract.
//
// The knowledge graph normally changes by rebuilding a whole account. The
// per-resource path (package resourcepatch, driven by the change detector and
// by collector events) changes one node instead. It may only do that when it
// produces exactly what the account build would, and what "exactly" means is a
// property of each resource type: which account-wide passes touch it, which of
// its edges come from its own row, which node types those edges point at.
//
// Each resource type states those facts once, next to the code that builds it,
// as a PatchSpec registered from the resource file's init(). Each cloud package
// turns its specs into a CloudPatchPolicy and registers it here. The applier
// and the detector only ever see CloudPatchPolicy: they hold no resource or
// cloud knowledge of their own.

// ResourceKind identifies a resource type the way the graph stores it: the
// cross-cloud ontology NodeType plus the concrete SpecificType (EC2Instance,
// SQSQueue, ...). Nodes synthesized by account-wide passes often carry no
// explicit specific type, in which case SpecificType equals the NodeType name.
type ResourceKind struct {
	NodeType     core.NodeType
	SpecificType string
}

// KindOf returns a node's ResourceKind.
func KindOf(node *core.DbNode) ResourceKind {
	return ResourceKind{NodeType: node.NodeType, SpecificType: node.SpecificType}
}

func (k ResourceKind) String() string {
	if k.SpecificType != "" {
		return k.SpecificType
	}
	return string(k.NodeType)
}

// PatchSpec is what one resource type declares about how it may be updated
// without an account rebuild.
//
// The zero value is the safe default for an undeclared type: properties may be
// refreshed from the inventory row, edges are left to the account build, and a
// new resource of the type is added by rebuilding the account.
type PatchSpec struct {
	// SpecificTypes are the specific types the spec covers. A name may also be a
	// NodeType name, which matches nodes synthesized without a specific type.
	SpecificTypes []string

	// AccountLevel marks types an account-wide pass creates, re-keys or
	// reshapes (inferred VPCs, load balancer slice rebuilds, CLI-fetched
	// identities). A single-row build cannot reproduce those, so every change
	// to such a resource rebuilds the account. Takes precedence over Creatable.
	AccountLevel bool

	// Creatable marks types whose node can be created from the rows of the one
	// resource. Only valid when every edge the full build gives the node either
	// is in Signature or points at it from another resource (those arrive at
	// the next account build).
	Creatable bool

	// Context lists the node types the type's edge builder resolves targets
	// against. The cloud policy adds whatever every key derivation needs.
	Context []core.NodeType

	// Signature is exactly the set of outgoing edges the type's edge builder
	// emits. A patch saves the rebuilt edges, then retires the node's old edges
	// that match the signature. It must never include an edge the builder does
	// not emit (a global pass's IS_ENCRYPTED_BY, for example), because the patch
	// would retire it and nothing would put it back.
	Signature core.EdgeSignature
}

// HasEdges reports whether the spec rewrites outgoing edges.
func (s PatchSpec) HasEdges() bool {
	return len(s.Signature.Kinds) > 0
}

// CloudPatchPolicy is one cloud's per-resource behaviour.
//
// Implementations are not safe for concurrent use: building nodes fills the
// source's per-build row cache. Create one per message or per detector run.
type CloudPatchPolicy interface {
	// Source is the node source the policy handles ("aws", "gcp", "azure").
	Source() string

	// IncludeForGraph reports whether the account build reads the row at all.
	IncludeForGraph(row *CloudResourceRow) bool

	// Classify builds the row's node in memory and returns its kind. ok is
	// false when the source builds no node from the row.
	Classify(row *CloudResourceRow, req *core.SourceBuildRequest) (kind ResourceKind, ok bool)

	// KindOf returns the kind the policy's specs are keyed on for a stored node.
	KindOf(node *core.DbNode) ResourceKind

	// Spec returns the spec declared for a kind, or the zero spec.
	Spec(kind ResourceKind) PatchSpec

	// ContextFor returns the node types to load from the graph for a patch of
	// the kind: what the cloud derives every unique key from (VPC names, for
	// example) plus the spec's own Context.
	ContextFor(kind ResourceKind) []core.NodeType

	// BuildNodes turns the inventory rows of one resource into nodes the way the
	// account build does, resolving key material against contextNodes. It also
	// returns references to network resources the account build would have to
	// infer a node for; any makes a patch unsafe.
	BuildNodes(rows []CloudResourceRow, contextNodes []*core.DbNode, req *core.SourceBuildRequest) (nodes []*core.DbNode, missingRefs []string)

	// EdgesBuiltElsewhere are edge kinds that leave this cloud's resources but
	// are built by an account-wide pass or another resource, so a patch must
	// neither emit nor retire them.
	EdgesBuiltElsewhere() []core.EdgeKind

	// OutgoingEdges rebuilds the node's own edges. ok is false for kinds without
	// an edge builder. newNodes are nodes the edges need that the builder
	// synthesizes (container images, for example). contextNodes must not
	// contain a copy of node.
	OutgoingEdges(node *core.DbNode, contextNodes []*core.DbNode, req *core.SourceBuildRequest) (edges []*core.DbEdge, newNodes []*core.DbNode, sig core.EdgeSignature, ok bool)
}

// CloudPatchPolicyFactory creates a policy. Called once per message or run.
type CloudPatchPolicyFactory func(logger *slog.Logger) CloudPatchPolicy

var (
	patchPolicyFactories   = make(map[string]CloudPatchPolicyFactory)
	patchPolicyFactoriesMu sync.RWMutex
)

// RegisterCloudPatchPolicy registers a cloud's policy factory. Call it from the
// cloud package's init().
func RegisterCloudPatchPolicy(source string, factory CloudPatchPolicyFactory) {
	patchPolicyFactoriesMu.Lock()
	defer patchPolicyFactoriesMu.Unlock()
	if source == "" || factory == nil {
		panic("cloud patch policy needs a source name and a factory")
	}
	if _, exists := patchPolicyFactories[source]; exists {
		panic(fmt.Sprintf("cloud patch policy %q is already registered", source))
	}
	patchPolicyFactories[source] = factory
}

// NewCloudPatchPolicies creates one policy per registered cloud, keyed by the
// node source it handles. The cloud packages must be imported (for their
// init side effect) by the binary.
func NewCloudPatchPolicies(logger *slog.Logger) map[string]CloudPatchPolicy {
	patchPolicyFactoriesMu.RLock()
	defer patchPolicyFactoriesMu.RUnlock()
	policies := make(map[string]CloudPatchPolicy, len(patchPolicyFactories))
	for source, factory := range patchPolicyFactories {
		policies[source] = factory(logger)
	}
	return policies
}

// SourceForCloudProvider maps a cloud_resourses.cloud_provider value ("AWS",
// "GCP", "Azure") to the node source its nodes carry.
func SourceForCloudProvider(cloudProvider string) string {
	return strings.ToLower(cloudProvider)
}

// PatchSpecRegistry holds a cloud's PatchSpecs together with the cloud-specific
// edge builder each one names. B is the cloud's edge-builder function type.
type PatchSpecRegistry[B any] struct {
	mu             sync.RWMutex
	byName         map[string]*registeredSpec[B]
	ordered        []*registeredSpec[B]
	builtElsewhere []core.EdgeKind
}

type registeredSpec[B any] struct {
	spec  PatchSpec
	build B
}

// NewPatchSpecRegistry returns an empty registry.
func NewPatchSpecRegistry[B any]() *PatchSpecRegistry[B] {
	return &PatchSpecRegistry[B]{byName: make(map[string]*registeredSpec[B])}
}

// Register adds a spec. It panics on a spec with no types, on a creatable
// account-level spec, on an edge signature without a builder (or the reverse)
// and on a type registered twice: all are programming errors in a resource
// file, and init-time panics surface them in every test run.
func (r *PatchSpecRegistry[B]) Register(spec PatchSpec, build B, hasBuilder bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(spec.SpecificTypes) == 0 {
		panic("patch spec declares no specific types")
	}
	if spec.AccountLevel && spec.Creatable {
		panic(fmt.Sprintf("patch spec %v is both account-level and creatable", spec.SpecificTypes))
	}
	if spec.HasEdges() != hasBuilder {
		panic(fmt.Sprintf("patch spec %v: an edge signature and an edge builder must be declared together", spec.SpecificTypes))
	}
	entry := &registeredSpec[B]{spec: spec, build: build}
	for _, name := range spec.SpecificTypes {
		if _, exists := r.byName[name]; exists {
			panic(fmt.Sprintf("patch spec for %q is already registered", name))
		}
		r.byName[name] = entry
	}
	r.ordered = append(r.ordered, entry)
}

// RegisterEdgesBuiltElsewhere declares edges that leave this cloud's resources
// but are built by something other than the resource itself — an account-wide
// pass, or another resource's builder. A patch signature must never contain
// one: the patch would retire it on every update and nothing in the patch path
// re-creates it. Declared by the pass that emits them, in its own file.
func (r *PatchSpecRegistry[B]) RegisterEdgesBuiltElsewhere(kinds ...core.EdgeKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builtElsewhere = append(r.builtElsewhere, kinds...)
}

// EdgesBuiltElsewhere returns those edge kinds, ordered by relationship then
// connection type.
func (r *PatchSpecRegistry[B]) EdgesBuiltElsewhere() []core.EdgeKind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.EdgeKind, len(r.builtElsewhere))
	copy(out, r.builtElsewhere)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Relationship != out[j].Relationship {
			return out[i].Relationship < out[j].Relationship
		}
		return out[i].ConnectionType < out[j].ConnectionType
	})
	return out
}

// Lookup returns the spec and builder for a kind: by specific type first, then
// by NodeType name. found is false for undeclared kinds, whose spec is zero.
func (r *PatchSpecRegistry[B]) Lookup(kind ResourceKind) (spec PatchSpec, build B, found bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if entry, ok := r.byName[kind.SpecificType]; ok {
		return entry.spec, entry.build, true
	}
	if entry, ok := r.byName[string(kind.NodeType)]; ok {
		return entry.spec, entry.build, true
	}
	return PatchSpec{}, build, false
}

// Specs returns every registered spec, ordered by first specific type.
func (r *PatchSpecRegistry[B]) Specs() []PatchSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]PatchSpec, 0, len(r.ordered))
	for _, entry := range r.ordered {
		out = append(out, entry.spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SpecificTypes[0] < out[j].SpecificTypes[0] })
	return out
}

// MergeNodeTypes returns base followed by the types of extra not already in it.
func MergeNodeTypes(base []core.NodeType, extra []core.NodeType) []core.NodeType {
	out := make([]core.NodeType, 0, len(base)+len(extra))
	seen := make(map[core.NodeType]bool, len(base)+len(extra))
	for _, list := range [][]core.NodeType{base, extra} {
		for _, t := range list {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// ErrNotInInventory reports that the resource a patch was asked about has no
// active inventory row: it was deleted.
var ErrNotInInventory = errors.New("resource has no active inventory row")

// WorkloadPatch is what the account build would produce for one Kubernetes
// workload row.
type WorkloadPatch struct {
	// Kind is the workload kind (Deployment, Service, ...).
	Kind string
	// Patchable is false for kinds whose node or edges also come from relay data
	// or other rows; for those only a delete can be applied without a rebuild,
	// and Node, Namespace, NewNodes and Edges are empty.
	Patchable bool
	Node      *core.DbNode
	// Namespace is the namespace node the workload runs on. The caller must
	// confirm it is already in the graph.
	Namespace *core.DbNode
	// NewNodes are nodes synthesized for the edges (repositories, Helm charts,
	// container images). Their keys are deterministic, so saving them upserts
	// the rows the account build wrote.
	NewNodes  []*core.DbNode
	Edges     []*core.DbEdge
	Signature core.EdgeSignature
}

// K8sPatchPolicy is the Kubernetes per-resource behaviour. Kubernetes nodes are
// built from k8s_workloads and k8s_nodes rather than cloud_resourses, so the
// policy reads those tables itself.
type K8sPatchPolicy interface {
	// Source is the node source the policy handles ("k8s").
	Source() string

	// DetectedWorkloadKinds are the workload kinds whose creation or deletion
	// the change detector watches: the graph's kinds minus the ones that churn
	// constantly.
	DetectedWorkloadKinds() []string

	// WorkloadSpecificTypes maps workload kinds to the specific types their
	// graph nodes carry.
	WorkloadSpecificTypes(kinds []string) []string

	// BuildWorkload reads one workload row and builds its node and own edges.
	// It returns ErrNotInInventory when the workload has no active row.
	BuildWorkload(tenantID, accountID, workloadID string) (*WorkloadPatch, error)

	// NodeSpecificType is the specific type of Kubernetes node graph nodes, which
	// carry no provider id and are addressed by name.
	NodeSpecificType() string

	// HasActiveNode reports whether the cluster still has an active Kubernetes
	// node with the name.
	HasActiveNode(tenantID, accountID, name string) (bool, error)
}

// K8sPatchPolicyFactory creates the Kubernetes policy.
type K8sPatchPolicyFactory func(logger *slog.Logger) (K8sPatchPolicy, error)

var k8sPatchPolicyFactory K8sPatchPolicyFactory

// RegisterK8sPatchPolicy registers the Kubernetes policy factory. Call it from
// the k8s package's init().
func RegisterK8sPatchPolicy(factory K8sPatchPolicyFactory) {
	patchPolicyFactoriesMu.Lock()
	defer patchPolicyFactoriesMu.Unlock()
	if factory == nil {
		panic("k8s patch policy factory cannot be nil")
	}
	if k8sPatchPolicyFactory != nil {
		panic("k8s patch policy is already registered")
	}
	k8sPatchPolicyFactory = factory
}

// NewK8sPatchPolicy creates the Kubernetes policy. The k8s package must be
// imported (for its init side effect) by the binary.
func NewK8sPatchPolicy(logger *slog.Logger) (K8sPatchPolicy, error) {
	patchPolicyFactoriesMu.RLock()
	factory := k8sPatchPolicyFactory
	patchPolicyFactoriesMu.RUnlock()
	if factory == nil {
		return nil, errors.New("no k8s patch policy registered: import the k8s source package")
	}
	return factory(logger)
}
