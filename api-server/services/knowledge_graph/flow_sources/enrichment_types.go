package flow_sources

import (
	"log/slog"
	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/security"
)

// AWS hostname pattern constants
const (
	AWSHostnameSuffix    = ".amazonaws.com"
	CloudfrontHostSuffix = ".cloudfront.net"
	// ECRPublicHost is the global hostname for AWS ECR Public; doesn't end in
	// `.amazonaws.com` so it needs its own membership test in IsAWSHostname.
	ECRPublicHost = "public.ecr.aws"
)

// EnrichmentMatchResult holds the result of matching an external service.
// RelationshipHint, when set, overrides the default RelationshipResolvesTo
// when createLinkEdge constructs the resulting edge — used by strategies that
// match on direct endpoint identity (e.g. ExternalService hostname == LB
// dns_name) and want to assert RelationshipRoutesThrough semantics rather
// than the looser RelationshipResolvesTo.
type EnrichmentMatchResult struct {
	Matched          bool
	Node             *core.DbNode
	MatchedBy        string
	RelationshipHint core.RelationshipType

	// ShadowDiff, when non-nil, reports that a stricter matching rule under
	// evaluation would have answered differently than the answer actually
	// returned above. It is purely observational — nothing reads it to decide
	// an edge — and exists so the stricter rule can be measured on real fleet
	// data before it is allowed to change any topology. Consumed by the
	// enricher, which records it to knowledge_graph_classification_review.
	ShadowDiff *MatchShadowDiff
}

// Reason codes for MatchShadowDiff. One code per distinct guard, per the
// convention in knowledge_graph/CLAUDE.md — a row labelled with a
// near-neighbour's code is worse than no row, because it sends whoever reads
// it looking for something that was never there.
const (
	// ShadowReasonNamespaceNotFound: the hostname carried a namespace, but no
	// node matched that name+namespace in any account. The current rule fell
	// back to matching on name alone and bound to some same-named node
	// elsewhere. This is the postgresql.nudgebee -> nudgebee-on-prem-test class.
	ShadowReasonNamespaceNotFound = "k8s_dns_namespace_not_found"
	// ShadowReasonNamespaceAmbiguous: name+namespace matched more than one node
	// across accounts, so the stricter rule refuses rather than picking one.
	ShadowReasonNamespaceAmbiguous = "k8s_dns_namespace_ambiguous"
	// ShadowReasonNoNamespace: no namespace could be parsed out of the hostname
	// (localhost / raw-IP shapes), leaving nothing to constrain the match.
	ShadowReasonNoNamespace = "k8s_dns_no_namespace"
	// ShadowReasonAWSEndpointShaped: the hostname is an AWS service endpoint,
	// including the region-truncated form the eBPF sensor emitted before
	// 2026-09-19 ("kafka.us-west" from kafka.us-west-1.amazonaws.com). Those
	// lost the "amazonaws" substring LooksLikeK8sServiceName screens on, so
	// they read as service.namespace. This is the demo/kafka class.
	ShadowReasonAWSEndpointShaped = "aws_endpoint_shaped_hostname"
	// ShadowReasonStricterPickedOther: both rules matched, but not the same
	// node — the stricter name+namespace rule found a better-qualified target
	// than the current name-only fallback did.
	ShadowReasonStricterPickedOther = "k8s_dns_stricter_picked_other_node"
)

// MatchShadowDiff captures a single disagreement between the current k8s-DNS
// matching ladder and the stricter one being evaluated. Only disagreements are
// recorded: when both rules agree — including when both decline, which is the
// common case for ordinary external hostnames — no diff is produced and
// nothing is written.
type MatchShadowDiff struct {
	Hostname        string
	ParsedService   string
	ParsedNamespace string
	ReasonCode      string

	// CurrentNodeKey is the node the live rule bound to ("" if it declined),
	// and CurrentNodeNamespace its namespace — the pair a reviewer needs to
	// see how wrong the current answer is.
	CurrentNodeKey       string
	CurrentNodeNamespace string
	CurrentMatchedBy     string

	// StrictNodeKey is what the stricter rule would bind to; "" means it would
	// decline and the edge would not exist.
	StrictNodeKey string

	// CandidateCount is how many nodes the stricter rule found for
	// name+namespace; >1 is what drives ShadowReasonNamespaceAmbiguous.
	CandidateCount int
}

// NoMatch returns an empty/unmatched result
func NoMatch() EnrichmentMatchResult {
	return EnrichmentMatchResult{Matched: false}
}

// Match returns a successful match result with no relationship-type override
// (createLinkEdge will use the default RelationshipResolvesTo).
func Match(node *core.DbNode, matchedBy string) EnrichmentMatchResult {
	return EnrichmentMatchResult{
		Matched:   true,
		Node:      node,
		MatchedBy: matchedBy,
	}
}

// MatchWithHint returns a successful match that also asks createLinkEdge to
// emit the given RelationshipType instead of the default RelationshipResolvesTo.
func MatchWithHint(node *core.DbNode, matchedBy string, hint core.RelationshipType) EnrichmentMatchResult {
	return EnrichmentMatchResult{
		Matched:          true,
		Node:             node,
		MatchedBy:        matchedBy,
		RelationshipHint: hint,
	}
}

// MatchingStrategy defines the interface for node matching strategies
type MatchingStrategy interface {
	Name() string
	Match(name string, ctx *MatchingContext) EnrichmentMatchResult
}

// MatchingContext holds context needed for matching.
// EndpointIndex is an O(1) endpoint→node lookup over LB/RDS/Cache nodes
// already in the graph; populated once per build by prepareMatchingContext
// and consumed by DirectEndpointMatchStrategy as the fast-path first
// strategy in the chain.
//
// K8sServiceIPResolver + CallerClusterIndex are populated once per account
// and consumed by K8sServiceIPMatchStrategy. CallerClusterIndex is keyed by
// the ExternalService node's `name` property (the IP string itself), which is
// unique within a per-account MatchingContext for IP-named ExternalServices.
// The map is immutable for the lifetime of ctx — no per-iteration mutation.
type MatchingContext struct {
	CloudAccountID    string
	EndpointIndex     map[string]endpointHit
	NodeMatcher       *NodeMatcher
	CloudResourcesMap map[string]*CloudResourceRow
	AWSAccountIDs     []string
	ZoneCache         *Route53ZoneCache
	RecordCache       *Route53RecordCache
	// Topology serves Route 53 lookups from cloud_resourses; nil means CLI-only.
	Topology             *CloudTopologyStore
	ReqCtx               *security.RequestContext
	Logger               *slog.Logger
	K8sServiceIPResolver *K8sServiceIPResolver
	CallerClusterIndex   map[string][]string
}
