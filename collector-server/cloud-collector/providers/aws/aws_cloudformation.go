package aws

import (
	"encoding/json"
	"errors"
	"nudgebee/collector/cloud/providers"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/smithy-go"
)

// Map CloudFormation stack status strings to provider status enum
func cfnStatusToNbStatus(status types.StackStatus) providers.ResourceStatus {
	s := string(status)
	switch {
	case strings.HasSuffix(s, "_COMPLETE") && !strings.HasPrefix(s, "DELETE_"):
		return providers.ResourceStatusActive
	case strings.HasSuffix(s, "_IN_PROGRESS"):
		return providers.ResourceStatusActive
	case strings.HasSuffix(s, "_FAILED"):
		return providers.ResourceStatusInactive
	case s == "DELETE_COMPLETE":
		return providers.ResourceStatusDeleted
	default:
		return providers.ResourceStatusUnknown
	}
}

type awsCloudFormation struct {
	DefaultAwsServiceImpl
}

func (a *awsCloudFormation) ApplyRecommendation(ctx providers.CloudProviderContext, account providers.Account, recommendation providers.Recommendation) error {
	// Applying recommendations is not supported for CloudFormation yet.
	return errors.ErrUnsupported
}

func (a *awsCloudFormation) ApplyCommand(ctx providers.CloudProviderContext, account providers.Account, command providers.ApplyCommandRequest) (providers.ApplyCommandResponse, error) {
	// Applying commands is not supported for CloudFormation yet.
	return providers.ApplyCommandResponse{}, errors.ErrUnsupported
}

func (a *awsCloudFormation) QueryMetrices(ctx providers.CloudProviderContext, account providers.Account, filter providers.QueryMetricsRequest) (providers.QueryMetricsResponse, error) {
	return getAwsCloudwatchMetrics(ctx, account, filter)
}

func (a *awsCloudFormation) GetResources(ctx providers.CloudProviderContext, account providers.Account, regionName string) ([]providers.Resource, error) {
	cfg, err := getAwsConfigFromAccount(ctx.GetContext(), account)
	if err != nil {
		ctx.GetLogger().Error("failed to create aws config", "error", err, "accountNumber", account.AccountNumber, "region", regionName, "service", ServiceNameCloudFormation)
		return []providers.Resource{}, err
	}
	cfg.Region = regionName
	svc := cloudformation.NewFromConfig(cfg)
	resources := []providers.Resource{}

	statusFilter := []types.StackStatus{
		types.StackStatusCreateInProgress,
		types.StackStatusCreateComplete,
		types.StackStatusRollbackInProgress,
		types.StackStatusRollbackFailed,
		types.StackStatusRollbackComplete,
		types.StackStatusDeleteInProgress,
		types.StackStatusDeleteFailed,
		types.StackStatusUpdateInProgress,
		types.StackStatusUpdateCompleteCleanupInProgress,
		types.StackStatusUpdateComplete,
		types.StackStatusUpdateRollbackInProgress,
		types.StackStatusUpdateRollbackFailed,
		types.StackStatusUpdateRollbackCompleteCleanupInProgress,
		types.StackStatusUpdateRollbackComplete,
		types.StackStatusReviewInProgress,
		types.StackStatusImportInProgress,
		types.StackStatusImportComplete,
		types.StackStatusImportRollbackInProgress,
		types.StackStatusImportRollbackFailed,
		types.StackStatusImportRollbackComplete,
	}

	paginator := cloudformation.NewListStacksPaginator(svc, &cloudformation.ListStacksInput{
		StackStatusFilter: statusFilter,
	})

	for paginator.HasMorePages() {
		stacksOutput, err := paginator.NextPage(ctx.GetContext())
		if err != nil {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "AuthFailure" || apiErr.ErrorCode() == "UnrecognizedClientException" || strings.Contains(apiErr.ErrorMessage(), "is not supported in this region")) {
				ctx.GetLogger().Warn("CloudFormation API might not be available in this region", "region", regionName, "error", apiErr.ErrorCode())
				break
			}
			ctx.GetLogger().Error("failed to list cloudformation stacks", "error", err, "accountNumber", account.AccountNumber, "region", regionName)
			return resources, err
		}

		for _, summary := range stacksOutput.StackSummaries {
			descOutput, err := svc.DescribeStacks(ctx.GetContext(), &cloudformation.DescribeStacksInput{
				StackName: summary.StackId,
			})
			if err != nil {
				ctx.GetLogger().Warn("failed to describe cloudformation stack", "error", err, "stackId", *summary.StackId, "region", regionName)
				continue
			}
			if len(descOutput.Stacks) == 0 {
				continue
			}
			stackDetails := descOutput.Stacks[0]

			tags := make(map[string][]string)
			for _, tag := range stackDetails.Tags {
				tags[aws.ToString(tag.Key)] = append(tags[aws.ToString(tag.Key)], aws.ToString(tag.Value))
			}

			resource := providers.Resource{
				Id:          aws.ToString(stackDetails.StackId),
				ServiceName: ServiceNameCloudFormation,
				Name:        aws.ToString(stackDetails.StackName),
				Status:      cfnStatusToNbStatus(stackDetails.StackStatus),
				Region:      regionName,
				Tags:        tags,
				Meta:        structToMap(stackDetails),
				Arn:         aws.ToString(stackDetails.StackId),
				CreatedAt:   aws.ToTime(stackDetails.CreationTime),
				Type:        getAwsServiceResourceType(ServiceNameCloudFormation, "stack"),
			}
			resources = append(resources, resource)
		}
	}

	return resources, nil
}

func (a *awsCloudFormation) GetRecommendations(ctx providers.CloudProviderContext, account providers.Account, filter providers.ListRecommendationsRequest, existingResources []providers.Resource) ([]providers.Recommendation, error) {
	recommendations := []providers.Recommendation{}
	cfg, err := getAwsConfigFromAccount(ctx.GetContext(), account)
	if err != nil {
		ctx.GetLogger().Error("failed to create aws config for recommendations", "error", err, "accountNumber", account.AccountNumber, "service", ServiceNameCloudFormation)
		return recommendations, err
	}

	for _, resource := range existingResources {
		if resource.Type != getAwsServiceResourceType(ServiceNameCloudFormation, "stack") {
			continue
		}

		meta := resource.Meta
		if len(meta) == 0 {
			continue
		}

		cfg.Region = resource.Region

		if driftInfo, ok := meta["DriftInformation"].(map[string]interface{}); ok {
			if status, ok := driftInfo["StackDriftStatus"].(string); ok {
				switch types.StackDriftStatus(status) {
				case types.StackDriftStatusNotChecked:
					recommendations = append(recommendations, providers.Recommendation{
						CategoryName:        providers.RecommendationCategoryConfiguration,
						RuleName:            "aws_cfn_drift_detection_check",
						Severity:            providers.RecommendationSeverityLow,
						Data:                map[string]any{"stack_name": resource.Name, "stack_arn": resource.Arn, "reason": "Drift status has not been checked."},
						Action:              providers.RecommendationActionModify,
						ResourceServiceName: resource.ServiceName,
						ResourceId:          resource.Id,
						ResourceType:        resource.Type,
						ResourceRegion:      resource.Region,
					})
				case types.StackDriftStatusDrifted:
					recommendations = append(recommendations, providers.Recommendation{
						CategoryName:        providers.RecommendationCategoryConfiguration,
						RuleName:            "aws_cfn_stack_drifted",
						Severity:            providers.RecommendationSeverityMedium,
						Data:                map[string]any{"stack_name": resource.Name, "stack_arn": resource.Arn, "reason": "Stack resources have drifted."},
						Action:              providers.RecommendationActionModify,
						ResourceServiceName: resource.ServiceName,
						ResourceId:          resource.Id,
						ResourceType:        resource.Type,
						ResourceRegion:      resource.Region,
					})
				}
			}
		}

		if enabled, ok := meta["EnableTerminationProtection"].(bool); ok && !enabled {
			recommendations = append(recommendations, providers.Recommendation{
				CategoryName:        providers.RecommendationCategoryConfiguration,
				RuleName:            "aws_cfn_termination_protection",
				Severity:            providers.RecommendationSeverityMedium,
				Data:                map[string]any{"stack_name": resource.Name, "stack_arn": resource.Arn},
				Action:              providers.RecommendationActionModify,
				ResourceServiceName: resource.ServiceName,
				ResourceId:          resource.Id,
				ResourceType:        resource.Type,
				ResourceRegion:      resource.Region,
			})
		}

		if policyBody, ok := meta["StackPolicyBody"].(string); !ok || policyBody == "" {
			recommendations = append(recommendations, providers.Recommendation{
				CategoryName:        providers.RecommendationCategorySecurity,
				RuleName:            "aws_cfn_stack_policy",
				Severity:            providers.RecommendationSeverityLow,
				Data:                map[string]any{"stack_name": resource.Name, "stack_arn": resource.Arn, "reason": "Stack does not have a stack policy."},
				Action:              providers.RecommendationActionModify,
				ResourceServiceName: resource.ServiceName,
				ResourceId:          resource.Id,
				ResourceType:        resource.Type,
				ResourceRegion:      resource.Region,
			})
		}

		if len(resource.Tags) == 0 {
			recommendations = append(recommendations, providers.Recommendation{
				CategoryName:        providers.RecommendationCategoryConfiguration,
				RuleName:            "aws_tags",
				Severity:            providers.RecommendationSeverityLow,
				Data:                map[string]any{"stack_name": resource.Name, "stack_arn": resource.Arn},
				Action:              providers.RecommendationActionModify,
				ResourceServiceName: resource.ServiceName,
				ResourceId:          resource.Id,
				ResourceType:        resource.Type,
				ResourceRegion:      resource.Region,
			})
		}
	}

	return recommendations, nil
}

func (a *awsCloudFormation) GetLogGroupName(ctx providers.CloudProviderContext, account providers.Account, region, resourceId string) (string, error) {
	cfg, err := getAwsConfigFromAccount(ctx.GetContext(), account)
	if err != nil {
		ctx.GetLogger().Error("failed to create aws config", "error", err, "accountNumber", account.AccountNumber)
		return "", err
	}
	cfg.Region = region
	logsSvc := cloudwatchlogs.NewFromConfig(cfg)

	paginator := cloudwatchlogs.NewDescribeLogGroupsPaginator(logsSvc, &cloudwatchlogs.DescribeLogGroupsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx.GetContext())
		if err != nil {
			return "", err
		}
		for _, lg := range page.LogGroups {
			logGroupName := aws.ToString(lg.LogGroupName)
			describeLogStreamsOutput, err := logsSvc.DescribeLogStreams(ctx.GetContext(), &cloudwatchlogs.DescribeLogStreamsInput{
				LogGroupName:        aws.String(logGroupName),
				LogStreamNamePrefix: aws.String(resourceId),
				Limit:               aws.Int32(1),
			})
			if err == nil && len(describeLogStreamsOutput.LogStreams) > 0 {
				return logGroupName, nil
			}
		}
	}
	return "", nil
}

func (a *awsCloudFormation) GetServiceMap(ctx providers.CloudProviderContext, account providers.Account, region, resourceId string) (providers.ServiceMapApplication, error) {
	app := providers.ServiceMapApplication{
		Id: providers.ServiceApplicationId{
			Name:      resourceId,
			Kind:      "cloudformation",
			Namespace: region,
		},
		Upstreams:   []providers.UpstreamLink{},
		Downstreams: []providers.DownstreamLink{},
		Status:      "Unknown",
	}

	// session, err := getAwsSessionFromAccount(account)
	// if err != nil {
	// 	ctx.GetLogger().Error("failed to create aws session", "error", err, "accountNumber", account.AccountNumber)
	// 	return providers.ServiceMapApplication{}, err
	// }

	return app, nil
}

// ---------------------------------------------------------------------------
// Stack -> managed-resource mapping
//
// The knowledge graph used to learn which resources a stack manages by asking
// this collector to shell out to `aws cloudformation list-stack-resources`, once
// per stack on every graph build. Each of those is a fresh Python interpreter.
// The list changes only when the stack itself changes, so it belongs in the
// stack's own row, captured once per discovery run.
// ---------------------------------------------------------------------------

const (
	// MetaKeyStackResourceSummaries is decoded by name in
	// api-server/services/knowledge_graph/sources/aws/cloudformation.go. The two
	// live in separate Go modules, so the compiler cannot catch a rename here --
	// the paired tests (aws_cloudformation_test.go on this side,
	// cloudformation_from_db_test.go on the graph's side) are the contract.
	//
	// Named for the ListStackResources response field on purpose: the graph's
	// existing CloudFormationStackResource struct then decodes this value
	// unchanged, with no new type and no second mapper to drift.
	MetaKeyStackResourceSummaries = "StackResourceSummaries"

	// MetaKeyStackResourcesCapturedAt is the change token the summaries above were
	// captured against -- NOT the stack's current token. Storing it separately is
	// what stops a failed ListStackResources from freezing a stale list forever;
	// see the failure rows in attachOneStack.
	MetaKeyStackResourcesCapturedAt = "StackResourcesCapturedAt"

	// maxStackResourceFetchesPerRun bounds the cold start. The first run after
	// deploy has no stored list for any stack, so every stack looks changed, and
	// ListStackResources is a roughly 1 TPS API -- several hundred sequential
	// calls would eat the sync budget. Stacks over budget keep whatever they had
	// (or stay absent, and the graph uses its CLI path for them), and the next run
	// picks up where this one stopped. Steady state is a handful of calls a day.
	maxStackResourceFetchesPerRun = 250
)

// stackResourceSummary is the trimmed ListStackResources row the knowledge graph
// reads. It mirrors CloudFormationStackResource in
// api-server/services/knowledge_graph/sources/aws/aws_source.go field for field.
//
// The SDK's types.StackResourceSummary carries five more fields. They are dropped
// for size, but mostly for churn: LastUpdatedTimestamp is per-resource and moves
// on any stack operation, and etl_resources.go rewrites meta wholesale each run,
// so carrying it would write a new row version every cycle for a stack nothing
// happened to. These four are stable for an unchanged stack.
type stackResourceSummary struct {
	LogicalResourceId  string `json:"LogicalResourceId"`
	PhysicalResourceId string `json:"PhysicalResourceId"`
	ResourceType       string `json:"ResourceType"`
	ResourceStatus     string `json:"ResourceStatus"`
}

// PriorStackResources is what the previous discovery run stored on one stack's row.
type PriorStackResources struct {
	CapturedAt string
	// Summaries is nil exactly when the key was absent. An empty JSON array is a
	// real answer -- a stack that genuinely manages nothing -- and must not be
	// collapsed into "absent", or that stack shells out on every graph build
	// forever.
	Summaries json.RawMessage
}

// StackResourceStats reports what AttachStackResourceSummaries did, for the log
// line the ETL emits. Without it a broken write is invisible: the graph keeps
// working off its CLI fallback while the saving silently never arrives.
type StackResourceStats struct {
	Stacks   int
	Reused   int
	Fetched  int
	Empty    int
	Failed   int
	Deferred int
}

// stackChangeToken returns the value the skip decision compares:
// COALESCE(LastUpdatedTime, CreationTime).
//
// Read out of the meta map rather than off types.Stack so that the token this run
// writes and the token the next run reads back out of cloud_resourses.meta are
// produced by one function over one representation, and cannot drift in timestamp
// formatting. An empty return means "no token", and therefore "never skip".
//
// Deletion is not this token's job: a deleted stack drops out of ListStacks and
// the ETL archives its row.
func stackChangeToken(meta map[string]any) string {
	for _, key := range []string{"LastUpdatedTime", "CreationTime"} {
		if s, ok := meta[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// listStackResources pages ListStackResources for one stack.
//
// Returns (nil, err) on failure and (non-nil, nil) on success, including a
// non-nil empty slice for a stack that genuinely manages nothing. Callers depend
// on that distinction: an empty list is stored, a failure is not.
//
// Takes the SDK's one-method ListStackResourcesAPIClient rather than *Client so a
// test can supply a mock without an injectable field on the provider.
func listStackResources(ctx providers.CloudProviderContext, client cloudformation.ListStackResourcesAPIClient, stackID string) ([]stackResourceSummary, error) {
	paginator := cloudformation.NewListStackResourcesPaginator(client, &cloudformation.ListStackResourcesInput{
		StackName: aws.String(stackID),
	})

	summaries := make([]stackResourceSummary, 0)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx.GetContext())
		if err != nil {
			// Abandon the stack rather than returning a partial list as success:
			// a truncated list would tombstone edges for resources the stack still
			// manages.
			return nil, err
		}
		for _, summary := range page.StackResourceSummaries {
			summaries = append(summaries, stackResourceSummary{
				LogicalResourceId:  aws.ToString(summary.LogicalResourceId),
				PhysicalResourceId: aws.ToString(summary.PhysicalResourceId),
				ResourceType:       aws.ToString(summary.ResourceType),
				ResourceStatus:     string(summary.ResourceStatus),
			})
		}
	}
	return summaries, nil
}

// stackPlan is one stack's decision, resolved before any AWS call so the budget
// can be applied across the whole account rather than per region.
type stackPlan struct {
	index    int // into the stacks slice passed to AttachStackResourceSummaries
	stackID  string
	region   string
	token    string
	prior    PriorStackResources
	hasPrior bool
}

// AttachStackResourceSummaries fills each stack's meta with the resources it
// manages, reusing the stored list when the stack's change token has not moved.
//
// It never returns an error, and never propagates one out of AWS. This is called
// from the ETL layer rather than from GetResources on purpose: GetResources runs
// inside main.go's per-region goroutine, where a returned throttle error reaches
// isThrottleError and makes the collector skip that whole region for the cycle.
// Out here the worst any failure can do is leave one stack's key absent, which
// the graph reads as "never captured" and answers with its existing CLI path.
func AttachStackResourceSummaries(ctx providers.CloudProviderContext, account providers.Account, stacks []providers.Resource, prior map[string]PriorStackResources) StackResourceStats {
	return attachStackResourceSummaries(ctx, account, stacks, prior, newCFNClientForRegion)
}

// cfnClientFactory builds a CloudFormation client for one region. Injected so the
// decision table below can be tested without AWS credentials.
type cfnClientFactory func(ctx providers.CloudProviderContext, account providers.Account, region string) (cloudformation.ListStackResourcesAPIClient, error)

func newCFNClientForRegion(ctx providers.CloudProviderContext, account providers.Account, region string) (cloudformation.ListStackResourcesAPIClient, error) {
	cfg, err := getAwsConfigFromAccount(ctx.GetContext(), account)
	if err != nil {
		return nil, err
	}
	cfg.Region = region
	return cloudformation.NewFromConfig(cfg), nil
}

func attachStackResourceSummaries(ctx providers.CloudProviderContext, account providers.Account, stacks []providers.Resource, prior map[string]PriorStackResources, newClient cfnClientFactory) StackResourceStats {
	stats := StackResourceStats{}
	logger := ctx.GetLogger()

	reuse := make([]stackPlan, 0, len(stacks))
	fetch := make([]stackPlan, 0, len(stacks))

	for i := range stacks {
		if stacks[i].Type != getAwsServiceResourceType(ServiceNameCloudFormation, "stack") {
			continue
		}
		stats.Stacks++
		if stacks[i].Meta == nil {
			stacks[i].Meta = map[string]any{}
		}
		p, hasPrior := prior[stacks[i].Id]
		plan := stackPlan{
			index:    i,
			stackID:  stacks[i].Id,
			region:   stacks[i].Region,
			token:    stackChangeToken(stacks[i].Meta),
			prior:    p,
			hasPrior: hasPrior && p.Summaries != nil,
		}
		// A stack with no token can never be proven unchanged, so it is always
		// refetched rather than being served from a list of unknown vintage.
		if plan.hasPrior && plan.token != "" && plan.prior.CapturedAt == plan.token {
			reuse = append(reuse, plan)
			continue
		}
		fetch = append(fetch, plan)
	}

	for _, plan := range reuse {
		carryForward(&stacks[plan.index], plan.prior)
		stats.Reused++
	}

	// Stacks with no stored list at all go first: they are the ones currently
	// costing the graph a CLI call, and a stack fetched this run leaves that group
	// for the next one, so the cold start drains.
	//
	// Among stacks that already have a list, refresh whichever was captured
	// longest ago. A static stack-id tie-break would let the lowest ids win the
	// budget every run, so an account with more changing stacks than the budget
	// would serve a permanently stale list for its tail. Comparing the token as a
	// string is only approximately chronological -- RFC3339Nano drops trailing
	// zeros, so two captures within the same second can invert -- which does not
	// matter for choosing what to refresh first. Stack id stays the final
	// tie-break, so the order is still total and deterministic.
	sort.SliceStable(fetch, func(i, j int) bool {
		if fetch[i].hasPrior != fetch[j].hasPrior {
			return !fetch[i].hasPrior
		}
		if fetch[i].prior.CapturedAt != fetch[j].prior.CapturedAt {
			return fetch[i].prior.CapturedAt < fetch[j].prior.CapturedAt
		}
		return fetch[i].stackID < fetch[j].stackID
	})

	if len(fetch) > maxStackResourceFetchesPerRun {
		for _, plan := range fetch[maxStackResourceFetchesPerRun:] {
			if plan.hasPrior {
				carryForward(&stacks[plan.index], plan.prior)
			}
			stats.Deferred++
		}
		fetch = fetch[:maxStackResourceFetchesPerRun]
		logger.Info("deferring CloudFormation stack resource fetches to the next run",
			"account_id", account.ID, "deferred", stats.Deferred, "budget", maxStackResourceFetchesPerRun)
	}

	clients := map[string]cloudformation.ListStackResourcesAPIClient{}
	for _, plan := range fetch {
		client, built := clients[plan.region]
		if !built {
			c, err := newClient(ctx, account, plan.region)
			if err != nil {
				logger.Warn("unable to build aws config for CloudFormation stack resources, skipping region",
					"error", err, "account_id", account.ID, "region", plan.region)
				c = nil
			}
			clients[plan.region] = c
			client = c
		}
		if client == nil {
			if plan.hasPrior {
				carryForward(&stacks[plan.index], plan.prior)
			}
			stats.Failed++
			continue
		}

		summaries, err := listStackResources(ctx, client, plan.stackID)
		if err != nil {
			// Swallowed deliberately -- see the function comment. On failure the
			// prior list is carried forward with its ORIGINAL token, never the
			// current one: writing the current token here would make the next run
			// believe the stale list is up to date and skip it forever.
			logger.Warn("failed to list CloudFormation stack resources, keeping what was stored",
				"error", err, "account_id", account.ID, "stack_id", plan.stackID, "region", plan.region)
			if plan.hasPrior {
				carryForward(&stacks[plan.index], plan.prior)
			}
			stats.Failed++
			continue
		}

		stacks[plan.index].Meta[MetaKeyStackResourceSummaries] = summaries
		if plan.token != "" {
			stacks[plan.index].Meta[MetaKeyStackResourcesCapturedAt] = plan.token
		}
		stats.Fetched++
		if len(summaries) == 0 {
			stats.Empty++
		}
	}

	return stats
}

// carryForward re-writes the stored list and the token it was captured against,
// unchanged. Both together or neither: the list is only meaningful alongside the
// token that says how old it is.
func carryForward(stack *providers.Resource, prior PriorStackResources) {
	if prior.Summaries == nil {
		return
	}
	stack.Meta[MetaKeyStackResourceSummaries] = prior.Summaries
	if prior.CapturedAt != "" {
		stack.Meta[MetaKeyStackResourcesCapturedAt] = prior.CapturedAt
	}
}
