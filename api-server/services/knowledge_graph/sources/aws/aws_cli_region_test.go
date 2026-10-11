package aws

import (
	"strings"
	"testing"
)

// A region-less `aws` command does not fail — it silently resolves against the
// collector's AWS_DEFAULT_REGION and reports resources in every other region as
// non-existent. These tests pin the region onto each region-scoped command so that
// regression cannot recur silently (#36573).

func TestBuildCFNListStackResourcesCommand(t *testing.T) {
	got := buildCFNListStackResourcesCommand("my-stack", "eu-west-2")
	want := "aws cloudformation list-stack-resources --stack-name my-stack --region eu-west-2 --output json"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestBuildSNSListSubscriptionsCommand(t *testing.T) {
	got := buildSNSListSubscriptionsCommand("us-west-2")
	want := "aws sns list-subscriptions --region us-west-2 --output json"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestBuildDescribeNATGatewaysCommand(t *testing.T) {
	got := buildDescribeNATGatewaysCommand("nat-0abc", "ap-south-1")
	want := "aws ec2 describe-nat-gateways --nat-gateway-ids nat-0abc --region ap-south-1 --output json"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestBuildDescribeMountTargetsCommand(t *testing.T) {
	got := buildDescribeMountTargetsCommand("fs-0abc", "eu-central-1")
	want := "aws efs describe-mount-targets --file-system-id fs-0abc --region eu-central-1 --output json"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// TestAllAWSRegionalCommandsCarryRegion is the class-level guard. Every region-scoped
// AWS CLI command built in this package must appear here; a new builder that forgets
// --region should fail this test rather than silently query the wrong region.
func TestAllAWSRegionalCommandsCarryRegion(t *testing.T) {
	const region = "eu-west-1"
	commands := map[string]string{
		"cloudformation list-stack-resources": buildCFNListStackResourcesCommand("stack", region),
		"sns list-subscriptions":              buildSNSListSubscriptionsCommand(region),
		"ec2 describe-nat-gateways":           buildDescribeNATGatewaysCommand("nat-0abc", region),
		"efs describe-mount-targets":          buildDescribeMountTargetsCommand("fs-0abc", region),
	}

	for name, cmd := range commands {
		if n := strings.Count(cmd, "--region "); n != 1 {
			t.Errorf("%s: expected exactly one --region flag, found %d in %q", name, n, cmd)
		}
		if !strings.Contains(cmd, "--region "+region) {
			t.Errorf("%s: command does not carry region %q: %q", name, region, cmd)
		}
		if !strings.Contains(cmd, "--output json") {
			t.Errorf("%s: command does not request json output: %q", name, cmd)
		}
	}
}

// TestCFNStackResourcesCacheKey pins the defect that made the command fix unsafe on
// its own: with the region missing from the key, two same-named stacks in different
// regions shared one cache entry and served each other's resource lists.
func TestCFNStackResourcesCacheKey(t *testing.T) {
	east := cfnStackResourcesCacheKey("acct-1", "us-east-1", "CDKToolkit")
	west := cfnStackResourcesCacheKey("acct-1", "eu-west-2", "CDKToolkit")

	if east == west {
		t.Errorf("same stack name in two regions collided on one cache key: %q", east)
	}
	for _, key := range []string{east, west} {
		for _, segment := range strings.Split(key, ":") {
			if segment == "" {
				t.Errorf("cache key has an empty segment: %q", key)
			}
		}
	}
}

// TestValidateAWSResourceID guards the CLI arguments that reach a shell command.
// Both the NAT gateway and EFS lookups fall back to the node's name when
// resource_id is empty, and a name derives from a customer-controlled tag.
func TestValidateAWSResourceID(t *testing.T) {
	valid := []string{
		"nat-0a1b2c3d4e5f67890",
		"fs-12345678",
		"fs-0a1b2c3d4e5f67890",
	}
	for _, id := range valid {
		if err := validateAWSResourceID(id); err != nil {
			t.Errorf("validateAWSResourceID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []string{
		"",
		"My NAT Gateway",                 // a Name tag, not an id
		"nat-0a1b; rm -rf /",             // command chaining
		"nat-0a1b2c3d && curl evil.test", // command chaining
		"nat-$(whoami)",                  // substitution
		"nat-`whoami`",                   // substitution
		"nat-0a1b2c3d4e5f67890extra",     // too long
		"NAT-0A1B2C3D",                   // uppercase
	}
	for _, id := range invalid {
		if err := validateAWSResourceID(id); err == nil {
			t.Errorf("validateAWSResourceID(%q) = nil, want an error", id)
		}
	}
}

// TestRegionScopedFetchersRejectBadInput pins that the fetchers refuse unsafe or
// missing arguments before any command is built.
func TestRegionScopedFetchersRejectBadInput(t *testing.T) {
	src, err := NewAWSSource(AWSSourceConfig{}, nil)
	if err != nil {
		t.Fatalf("NewAWSSource: %v", err)
	}

	if _, err := src.fetchNATGatewayDataFromAWS(nil, "a1", "us-east-1", "nat-0a1b; rm -rf /"); err == nil {
		t.Error("NAT gateway fetch accepted an injected id")
	}
	if _, err := src.fetchNATGatewayDataFromAWS(nil, "a1", "", "nat-0a1b2c3d"); err == nil {
		t.Error("NAT gateway fetch accepted an empty region")
	}
	if _, err := src.fetchEFSMountTargetsFromAWS(nil, "a1", "us-east-1", "fs-1; whoami"); err == nil {
		t.Error("EFS fetch accepted an injected id")
	}
	if _, err := src.fetchEFSMountTargetsFromAWS(nil, "a1", "not-a-region", "fs-12345678"); err == nil {
		t.Error("EFS fetch accepted a malformed region")
	}
	if _, err := src.fetchSNSSubscriptionsForRegion(nil, "a1", "us-east-1; whoami"); err == nil {
		t.Error("SNS fetch accepted a malformed region")
	}
}
