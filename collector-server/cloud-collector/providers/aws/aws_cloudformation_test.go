package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"nudgebee/collector/cloud/providers"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStackResourceSummaryMetaKeys pins the exact meta keys this writer emits.
// The knowledge graph decodes these by name in
// api-server/services/knowledge_graph/sources/aws/cloudformation.go, and the two
// live in separate Go modules so the compiler cannot catch a rename. This test is
// the contract: change a key here and it fails, rather than the graph silently
// losing every MANAGES edge.
func TestStackResourceSummaryMetaKeys(t *testing.T) {
	summary := stackResourceSummary{
		LogicalResourceId:  "AppBucket",
		PhysicalResourceId: "my-app-bucket",
		ResourceType:       "AWS::S3::Bucket",
		ResourceStatus:     "CREATE_COMPLETE",
	}

	encoded, err := json.Marshal(summary)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	assert.Equal(t, map[string]any{
		"LogicalResourceId":  "AppBucket",
		"PhysicalResourceId": "my-app-bucket",
		"ResourceType":       "AWS::S3::Bucket",
		"ResourceStatus":     "CREATE_COMPLETE",
	}, decoded)

	// The five SDK fields we deliberately drop must stay dropped. Restoring
	// LastUpdatedTimestamp in particular would make the stored value differ
	// byte-for-byte on every discovery run for a stack nothing happened to, and
	// etl_resources.go rewrites meta wholesale, so the row would churn forever.
	for _, dropped := range []string{"LastUpdatedTimestamp", "ResourceStatusReason", "DriftInformation", "ModuleInfo", "PhysicalResourceIdContext"} {
		assert.NotContains(t, decoded, dropped, "dropped SDK field reappeared in the stored payload")
	}
}

// TestMetaKeyNamesAreStable is the other half of the contract: the constants
// themselves. cloudformation_from_db_test.go on the graph's side asserts the same
// two literals.
func TestMetaKeyNamesAreStable(t *testing.T) {
	assert.Equal(t, "StackResourceSummaries", MetaKeyStackResourceSummaries)
	assert.Equal(t, "StackResourcesCapturedAt", MetaKeyStackResourcesCapturedAt)
}

func TestStackChangeToken(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want string
	}{
		{"last updated wins over creation", map[string]any{
			"CreationTime": "2026-01-01T00:00:00Z", "LastUpdatedTime": "2026-06-01T00:00:00Z",
		}, "2026-06-01T00:00:00Z"},
		{"creation alone is used", map[string]any{
			"CreationTime": "2026-01-01T00:00:00Z",
		}, "2026-01-01T00:00:00Z"},
		{"neither means never skip", map[string]any{"StackName": "app"}, ""},
		{"empty last updated falls through to creation", map[string]any{
			"LastUpdatedTime": "", "CreationTime": "2026-01-01T00:00:00Z",
		}, "2026-01-01T00:00:00Z"},
		{"non-string value is ignored", map[string]any{"LastUpdatedTime": 1234}, ""},
		{"nil meta", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, stackChangeToken(tc.meta))
		})
	}
}

// TestStackChangeTokenRoundTripsThroughStructToMap is the test that stops the
// write-side and read-side token formats from silently diverging: the token is
// written from a freshly-described stack and read back from the jsonb round-trip,
// and the two must be the same string.
func TestStackChangeTokenRoundTripsThroughStructToMap(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	updated := time.Date(2026, 6, 7, 8, 9, 10, 987654321, time.UTC)
	stack := types.Stack{
		StackId:         aws.String("arn:aws:cloudformation:us-east-1:123456789012:stack/app/abc"),
		StackName:       aws.String("app"),
		CreationTime:    aws.Time(created),
		LastUpdatedTime: aws.Time(updated),
	}

	atWrite := structToMap(stack)
	writeToken := stackChangeToken(atWrite)
	require.NotEmpty(t, writeToken)

	// Simulate the jsonb round-trip the value makes through cloud_resourses.meta.
	encoded, err := json.Marshal(atWrite)
	require.NoError(t, err)
	var atRead map[string]any
	require.NoError(t, json.Unmarshal(encoded, &atRead))

	assert.Equal(t, writeToken, stackChangeToken(atRead),
		"token must survive the jsonb round-trip unchanged, or every stack looks changed on every run")
}

// mockCFNClient satisfies cloudformation.ListStackResourcesAPIClient, which the
// SDK already declares as a one-method interface -- no injectable field on the
// provider is needed.
type mockCFNClient struct {
	pages []*cloudformation.ListStackResourcesOutput
	err   error
	calls int
}

func (m *mockCFNClient) ListStackResources(_ context.Context, _ *cloudformation.ListStackResourcesInput, _ ...func(*cloudformation.Options)) (*cloudformation.ListStackResourcesOutput, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	if m.calls-1 < len(m.pages) {
		return m.pages[m.calls-1], nil
	}
	return &cloudformation.ListStackResourcesOutput{}, nil
}

func testStack(id, region, token string) providers.Resource {
	return providers.Resource{
		Id:     id,
		Region: region,
		Type:   getAwsServiceResourceType(ServiceNameCloudFormation, "stack"),
		Meta:   map[string]any{"StackName": id, "CreationTime": token},
	}
}

func onePage(logicalID, physicalID string) []*cloudformation.ListStackResourcesOutput {
	return []*cloudformation.ListStackResourcesOutput{{
		StackResourceSummaries: []types.StackResourceSummary{{
			LogicalResourceId:  aws.String(logicalID),
			PhysicalResourceId: aws.String(physicalID),
			ResourceType:       aws.String("AWS::S3::Bucket"),
			ResourceStatus:     types.ResourceStatusCreateComplete,
		}},
	}}
}

func factoryFor(client cloudformation.ListStackResourcesAPIClient) cfnClientFactory {
	return func(providers.CloudProviderContext, providers.Account, string) (cloudformation.ListStackResourcesAPIClient, error) {
		return client, nil
	}
}

func TestAttachStackResourceSummariesUnchangedTokenSkipsTheCall(t *testing.T) {
	ctx := providers.NewCloudProviderContext(context.Background())
	stacks := []providers.Resource{testStack("stack-a", "us-east-1", "T1")}
	prior := map[string]PriorStackResources{
		"stack-a": {CapturedAt: "T1", Summaries: json.RawMessage(`[{"LogicalResourceId":"B"}]`)},
	}
	client := &mockCFNClient{}

	stats := attachStackResourceSummaries(ctx, providers.Account{ID: "acct"}, stacks, prior, factoryFor(client))

	assert.Equal(t, 0, client.calls, "an unchanged stack must not be queried")
	assert.Equal(t, 1, stats.Reused)
	assert.Equal(t, json.RawMessage(`[{"LogicalResourceId":"B"}]`), stacks[0].Meta[MetaKeyStackResourceSummaries])
	assert.Equal(t, "T1", stacks[0].Meta[MetaKeyStackResourcesCapturedAt])
}

func TestAttachStackResourceSummariesMovedTokenRefetches(t *testing.T) {
	ctx := providers.NewCloudProviderContext(context.Background())
	stacks := []providers.Resource{testStack("stack-a", "us-east-1", "T2")}
	prior := map[string]PriorStackResources{
		"stack-a": {CapturedAt: "T1", Summaries: json.RawMessage(`[{"LogicalResourceId":"Old"}]`)},
	}
	client := &mockCFNClient{pages: onePage("New", "bucket-new")}

	stats := attachStackResourceSummaries(ctx, providers.Account{ID: "acct"}, stacks, prior, factoryFor(client))

	assert.Equal(t, 1, client.calls)
	assert.Equal(t, 1, stats.Fetched)
	summaries := stacks[0].Meta[MetaKeyStackResourceSummaries].([]stackResourceSummary)
	require.Len(t, summaries, 1)
	assert.Equal(t, "New", summaries[0].LogicalResourceId)
	assert.Equal(t, "T2", stacks[0].Meta[MetaKeyStackResourcesCapturedAt])
}

// TestAttachStackResourceSummariesFailedFetchKeepsTheOldToken is the stale-freeze
// regression. Writing the CURRENT token alongside a carried-forward list would
// make the next run compare equal, skip, and serve that stale list forever.
func TestAttachStackResourceSummariesFailedFetchKeepsTheOldToken(t *testing.T) {
	ctx := providers.NewCloudProviderContext(context.Background())
	stacks := []providers.Resource{testStack("stack-a", "us-east-1", "T2")}
	prior := map[string]PriorStackResources{
		"stack-a": {CapturedAt: "T1", Summaries: json.RawMessage(`[{"LogicalResourceId":"Old"}]`)},
	}
	client := &mockCFNClient{err: &smithy.GenericAPIError{Code: "ValidationError", Message: "boom"}}

	stats := attachStackResourceSummaries(ctx, providers.Account{ID: "acct"}, stacks, prior, factoryFor(client))

	assert.Equal(t, 1, stats.Failed)
	assert.Equal(t, json.RawMessage(`[{"LogicalResourceId":"Old"}]`), stacks[0].Meta[MetaKeyStackResourceSummaries])
	assert.Equal(t, "T1", stacks[0].Meta[MetaKeyStackResourcesCapturedAt],
		"the token must stay at the vintage of the list actually held, so the next run retries")
}

func TestAttachStackResourceSummariesFailedFetchWithNoPriorWritesNeitherKey(t *testing.T) {
	ctx := providers.NewCloudProviderContext(context.Background())
	stacks := []providers.Resource{testStack("stack-a", "us-east-1", "T1")}
	client := &mockCFNClient{err: &smithy.GenericAPIError{Code: "ValidationError", Message: "boom"}}

	stats := attachStackResourceSummaries(ctx, providers.Account{ID: "acct"}, stacks, map[string]PriorStackResources{}, factoryFor(client))

	assert.Equal(t, 1, stats.Failed)
	_, hasList := stacks[0].Meta[MetaKeyStackResourceSummaries]
	_, hasToken := stacks[0].Meta[MetaKeyStackResourcesCapturedAt]
	assert.False(t, hasList, "an absent key is what tells the graph to use its CLI path")
	assert.False(t, hasToken)
}

// TestAttachStackResourceSummariesEmptyIsStored: a stack that genuinely manages
// nothing gets a real empty answer, so it never queries again.
func TestAttachStackResourceSummariesEmptyIsStored(t *testing.T) {
	ctx := providers.NewCloudProviderContext(context.Background())
	stacks := []providers.Resource{testStack("stack-a", "us-east-1", "T1")}
	client := &mockCFNClient{pages: []*cloudformation.ListStackResourcesOutput{{}}}

	stats := attachStackResourceSummaries(ctx, providers.Account{ID: "acct"}, stacks, map[string]PriorStackResources{}, factoryFor(client))

	assert.Equal(t, 1, stats.Fetched)
	assert.Equal(t, 1, stats.Empty)
	stored, ok := stacks[0].Meta[MetaKeyStackResourceSummaries]
	require.True(t, ok, "an empty list is a real answer and must be stored")
	assert.Empty(t, stored.([]stackResourceSummary))
}

func TestAttachStackResourceSummariesNoTokenAlwaysFetches(t *testing.T) {
	ctx := providers.NewCloudProviderContext(context.Background())
	stacks := []providers.Resource{testStack("stack-a", "us-east-1", "")}
	prior := map[string]PriorStackResources{
		"stack-a": {CapturedAt: "", Summaries: json.RawMessage(`[]`)},
	}
	client := &mockCFNClient{pages: onePage("New", "bucket-new")}

	attachStackResourceSummaries(ctx, providers.Account{ID: "acct"}, stacks, prior, factoryFor(client))

	assert.Equal(t, 1, client.calls, "a stack with no token can never be proven unchanged")
	_, hasToken := stacks[0].Meta[MetaKeyStackResourcesCapturedAt]
	assert.False(t, hasToken, "no token means never skip on the next run either")
}

// TestAttachStackResourceSummariesNeverPropagates is the region-skip guard as an
// executable assertion: a throttle error escaping this code would reach
// isThrottleError in main.go and make the collector skip the whole region.
func TestAttachStackResourceSummariesNeverPropagates(t *testing.T) {
	ctx := providers.NewCloudProviderContext(context.Background())
	stacks := []providers.Resource{testStack("stack-a", "us-east-1", "T1")}
	throttle := &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}
	require.True(t, isThrottleError(throttle), "fixture must be a real throttle error")

	stats := attachStackResourceSummaries(ctx, providers.Account{ID: "acct"}, stacks,
		map[string]PriorStackResources{}, factoryFor(&mockCFNClient{err: throttle}))

	assert.Equal(t, 1, stats.Failed, "the throttle is absorbed into a counter, never returned")
}

func TestAttachStackResourceSummariesBudgetDefersAndKeepsPrior(t *testing.T) {
	ctx := providers.NewCloudProviderContext(context.Background())
	stacks := make([]providers.Resource, 0, maxStackResourceFetchesPerRun+2)
	prior := map[string]PriorStackResources{}
	for i := 0; i < maxStackResourceFetchesPerRun+2; i++ {
		id := fmt.Sprintf("stack-%04d", i)
		stacks = append(stacks, testStack(id, "us-east-1", "T2"))
		prior[id] = PriorStackResources{CapturedAt: "T1", Summaries: json.RawMessage(`[]`)}
	}
	client := &mockCFNClient{}

	stats := attachStackResourceSummaries(ctx, providers.Account{ID: "acct"}, stacks, prior, factoryFor(client))

	assert.Equal(t, maxStackResourceFetchesPerRun, client.calls, "the run must stop at the budget")
	assert.Equal(t, 2, stats.Deferred)
	// A deferred stack keeps what it had, at its original token, so it is retried.
	last := stacks[len(stacks)-1]
	assert.Equal(t, json.RawMessage(`[]`), last.Meta[MetaKeyStackResourceSummaries])
	assert.Equal(t, "T1", last.Meta[MetaKeyStackResourcesCapturedAt])
}

func TestListStackResourcesPaginates(t *testing.T) {
	ctx := providers.NewCloudProviderContext(context.Background())
	client := &mockCFNClient{pages: []*cloudformation.ListStackResourcesOutput{
		{
			NextToken: aws.String("page2"),
			StackResourceSummaries: []types.StackResourceSummary{{
				LogicalResourceId: aws.String("One"), PhysicalResourceId: aws.String("p1"),
				ResourceType: aws.String("AWS::S3::Bucket"), ResourceStatus: types.ResourceStatusCreateComplete,
			}},
		},
		{
			StackResourceSummaries: []types.StackResourceSummary{{
				LogicalResourceId: aws.String("Two"), PhysicalResourceId: aws.String("p2"),
				ResourceType: aws.String("AWS::SQS::Queue"), ResourceStatus: types.ResourceStatusDeleteComplete,
			}},
		},
	}}

	got, err := listStackResources(ctx, client, "stack-a")

	require.NoError(t, err)
	require.Len(t, got, 2, "both pages must be collected")
	assert.Equal(t, "One", got[0].LogicalResourceId)
	// The writer trims but does not filter: DELETE_COMPLETE is stored, and the
	// graph applies the same skip it always did.
	assert.Equal(t, "DELETE_COMPLETE", got[1].ResourceStatus)
}

func TestListStackResourcesAbandonsStackOnPageError(t *testing.T) {
	ctx := providers.NewCloudProviderContext(context.Background())
	client := &mockCFNClient{err: &smithy.GenericAPIError{Code: "ValidationError"}}

	got, err := listStackResources(ctx, client, "stack-a")

	require.Error(t, err)
	assert.Nil(t, got, "a partial list must never be returned as success")
}

// TestAttachStackResourceSummariesRefreshesOldestFirst guards against starving the
// tail of a large account. Once every stack has a stored list, a static stack-id
// tie-break would hand the budget to the same lowest ids on every run, and the
// stacks sorted last would serve a stale list forever. Priority is the capture
// token, oldest first, so the staleest stacks are the ones refreshed.
func TestAttachStackResourceSummariesRefreshesOldestFirst(t *testing.T) {
	ctx := providers.NewCloudProviderContext(context.Background())

	// Ids ascend while capture times descend, so id order and staleness order are
	// exact opposites -- the old comparator picked precisely the wrong ones.
	total := maxStackResourceFetchesPerRun + 3
	stacks := make([]providers.Resource, 0, total)
	prior := map[string]PriorStackResources{}
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("stack-%04d", i)
		stacks = append(stacks, testStack(id, "us-east-1", "current"))
		// Fixed-width so lexicographic order is chronological order, which is what
		// the comparator relies on.
		n := total - i
		prior[id] = PriorStackResources{
			CapturedAt: fmt.Sprintf("2026-01-01T00:%02d:%02dZ", n/60, n%60),
			Summaries:  json.RawMessage(`[]`),
		}
	}

	client := &recordingCFNClient{}
	stats := attachStackResourceSummaries(ctx, providers.Account{ID: "acct"}, stacks, prior,
		func(providers.CloudProviderContext, providers.Account, string) (cloudformation.ListStackResourcesAPIClient, error) {
			return client, nil
		})
	fetched := client.stacks

	assert.Equal(t, maxStackResourceFetchesPerRun, stats.Fetched)
	assert.Equal(t, 3, stats.Deferred)

	// The three highest ids hold the oldest captures, so they must be fetched --
	// under the id tie-break they would have been the three deferred instead.
	for _, mustFetch := range []string{
		fmt.Sprintf("stack-%04d", total-1),
		fmt.Sprintf("stack-%04d", total-2),
		fmt.Sprintf("stack-%04d", total-3),
	} {
		assert.Contains(t, fetched, mustFetch, "the stalest stacks must win the budget")
	}
	// And the freshest stack is the one that waits.
	assert.NotContains(t, fetched, "stack-0000", "the most recently captured stack should defer")
}

// recordingCFNClient records which stacks were asked for.
type recordingCFNClient struct{ stacks []string }

func (m *recordingCFNClient) ListStackResources(_ context.Context, in *cloudformation.ListStackResourcesInput, _ ...func(*cloudformation.Options)) (*cloudformation.ListStackResourcesOutput, error) {
	m.stacks = append(m.stacks, aws.ToString(in.StackName))
	return &cloudformation.ListStackResourcesOutput{}, nil
}
