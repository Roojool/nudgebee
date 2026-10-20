package account

import (
	"bytes"
	"encoding/json"
	"nudgebee/collector/cloud/common"
	"nudgebee/collector/cloud/providers"
	awsProvider "nudgebee/collector/cloud/providers/aws"
	"nudgebee/collector/cloud/security"
)

// enrichCloudFormationStacks is the DB half of the stack -> managed-resource
// mapping. It reads what the previous run stored and hands it to the provider,
// which decides per stack whether to call AWS again.
//
// It never returns an error. Every failure here leaves stacks without the meta
// key, and an absent key is exactly the signal the knowledge graph reads as
// "never captured" -- it then answers from its own CLI path, which is today's
// behaviour. Degrading to that is always safe; failing the discovery run is not.
//
// Mutates item.Meta in place, so the keys ride the existing
// `meta = EXCLUDED.meta` upsert with no SQL change.
func enrichCloudFormationStacks(ctx *security.RequestContext, dbms *common.DatabaseManager, accountId string, account providers.Account, items []providers.Resource) {
	prior := loadPriorStackResources(ctx, dbms, accountId)
	stats := awsProvider.AttachStackResourceSummaries(ctx, account, items, prior)
	ctx.GetLogger().Info("captured CloudFormation stack resources",
		"account_id", accountId,
		"stacks", stats.Stacks,
		"reused", stats.Reused,
		"fetched", stats.Fetched,
		"empty", stats.Empty,
		"failed", stats.Failed,
		"deferred", stats.Deferred)
}

// priorStackRow is one stack's stored state, as scanned out of cloud_resourses.
type priorStackRow struct {
	ResourceID string          `db:"resourse_id"`
	CapturedAt string          `db:"captured_at"`
	Summaries  json.RawMessage `db:"summaries"`
}

// loadPriorStackResources returns, per stack id, the list and capture token the
// last discovery run stored.
//
// Selects two expressions out of meta rather than the whole column: a stack row
// carries the entire DescribeStacks payload (Parameters, Outputs, Tags), and
// dragging all of that back for several hundred stacks to read two fields is the
// kind of waste this change exists to remove.
func loadPriorStackResources(ctx *security.RequestContext, dbms *common.DatabaseManager, accountId string) map[string]awsProvider.PriorStackResources {
	prior := map[string]awsProvider.PriorStackResources{}

	var rows []priorStackRow
	query := `select resourse_id,
	                 coalesce(meta->>'StackResourcesCapturedAt', '') as captured_at,
	                 meta->'StackResourceSummaries'                  as summaries
	            from cloud_resourses
	           where account = $1
	             and lower(service_name) = 'awscloudformation'
	             and type = 'stack'
	             and is_active = true`
	if err := dbms.QueryAndScan(&rows, query, accountId); err != nil {
		// An empty map degrades to "fetch everything", which is correct, just
		// slower -- and the per-run budget keeps that bounded.
		ctx.GetLogger().Warn("unable to load stored CloudFormation stack resources, will refetch",
			"error", err, "account_id", accountId)
		return prior
	}

	return decodePriorStackRows(ctx, accountId, rows)
}

// decodePriorStackRows turns scanned rows into the prior map, rejecting anything
// the writer would not have produced. Split from the query so it is testable
// without a database.
func decodePriorStackRows(ctx *security.RequestContext, accountId string, rows []priorStackRow) map[string]awsProvider.PriorStackResources {
	prior := map[string]awsProvider.PriorStackResources{}
	for _, row := range rows {
		// A SQL NULL scans into a nil RawMessage, meaning the key was absent. A
		// JSON null arrives as the four bytes `null`: the writer's contract is
		// "omit the key or write an array", so a literal null is evidence the
		// writer misbehaved and is treated as absent rather than trusted.
		if len(row.Summaries) == 0 {
			continue
		}
		trimmed := bytes.TrimSpace(row.Summaries)
		if bytes.Equal(trimmed, []byte("null")) {
			ctx.GetLogger().Warn("stored CloudFormation stack resources are null, refetching",
				"account_id", accountId, "stack_id", row.ResourceID)
			continue
		}
		if len(trimmed) == 0 || trimmed[0] != '[' {
			ctx.GetLogger().Warn("stored CloudFormation stack resources are not an array, refetching",
				"account_id", accountId, "stack_id", row.ResourceID)
			continue
		}
		prior[row.ResourceID] = awsProvider.PriorStackResources{
			CapturedAt: row.CapturedAt,
			Summaries:  trimmed,
		}
	}
	return prior
}
