package aws

import (
	"sort"
	"strings"

	"nudgebee/services/knowledge_graph/core"
	"nudgebee/services/knowledge_graph/sources"
)

// Region resolution for region-scoped AWS CLI lookups.
//
// A region-less `aws` command does not fail — it silently resolves against the
// cloud collector's AWS_DEFAULT_REGION. For a resource that lives elsewhere the
// answer comes back as "does not exist", which is indistinguishable from a
// genuinely deleted resource. That ambiguity is what made #36573 invisible for
// so long, so callers must resolve a real region before issuing the command and
// skip the call when they cannot.

// nodeRegion returns the validated AWS region a node lives in, or "" when the
// node carries no usable region.
//
// "" means "the caller must decide" — there is deliberately no default. Callers
// that issue a CLI command must skip; callers that build a node may keep "".
func nodeRegion(node *core.DbNode) string {
	if node == nil {
		return ""
	}

	// 1. The region stamped by createNodeFromResource from cloud_resourses.region.
	//    Covers every node built through the classify path.
	if region, ok := core.GetNodePropertyString(node, "region"); ok {
		if validAWSRegionRegex.MatchString(region) {
			return region
		}
	}

	// 2. Derive it from the AZ. Mirrors the probe order core.extractNodeLocation
	//    already uses, and covers nodes that record only an AZ (eg ENIs).
	if az, ok := core.GetNodePropertyString(node, "availability_zone"); ok {
		if region := regionFromAZ(az); region != "" {
			return region
		}
	}

	// 3. Derive it from the ARN, for rows whose cloud_resourses.region is NULL.
	if arn, ok := core.GetNodePropertyString(node, "arn"); ok {
		region := sources.GetRegionFromARN(arn)
		if validAWSRegionRegex.MatchString(region) {
			return region
		}
	}

	// 4. The unique key's Location segment, written by GenerateUniqueKey. Mirrors
	//    the fallback in flow_sources.regionsForAccount.
	if parts := strings.Split(node.UniqueKey, ":"); len(parts) > 2 {
		if validAWSRegionRegex.MatchString(parts[2]) {
			return parts[2]
		}
	}

	return ""
}

// nodeRegions returns the distinct validated regions of a node set.
//
// The result is sorted: callers issue one CLI call per region, and map iteration
// order would make that call sequence non-deterministic between runs.
func nodeRegions(nodes []*core.DbNode) []string {
	seen := make(map[string]struct{})
	for _, node := range nodes {
		if region := nodeRegion(node); region != "" {
			seen[region] = struct{}{}
		}
	}

	regions := make([]string, 0, len(seen))
	for region := range seen {
		regions = append(regions, region)
	}
	sort.Strings(regions)
	return regions
}

// regionFromAZ returns the region an availability zone belongs to
// ("us-east-1a" -> "us-east-1"). A value that is already a region is returned
// unchanged; anything else returns "".
func regionFromAZ(az string) string {
	if az == "" {
		return ""
	}
	if validAWSRegionRegex.MatchString(az) {
		return az
	}
	// AZs are the region plus a single trailing letter.
	region := strings.TrimRight(az, "abcdefghijklmnopqrstuvwxyz")
	if validAWSRegionRegex.MatchString(region) {
		return region
	}
	return ""
}
