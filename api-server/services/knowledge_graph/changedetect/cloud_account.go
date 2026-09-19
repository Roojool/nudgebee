package changedetect

import (
	"nudgebee/services/knowledge_graph/core"
	kgqueue "nudgebee/services/knowledge_graph/queue"
	"nudgebee/services/knowledge_graph/sources"
)

// probeCloudAccount checks one AWS, GCP or Azure account.
//
//   - Rows created since the mark: each one the graph reads must be of a type its
//     policy declares creatable (an upsert ref), otherwise the account rebuilds.
//   - The active count, net of those rows: fewer means resources were deleted
//     (their nodes are named and get delete refs); more means old rows came back
//     (the account rebuilds).
func (d *Detector) probeCloudAccount(repo *repository, policy sources.CloudPatchPolicy, tenantID, accountID string, prev, next *Marks, stats *runStats) (*accountResult, error) {
	probe, err := repo.probeCloud(accountID)
	if err != nil {
		return nil, err
	}
	decision := DecideCloud(prev, probe)
	next.CloudActiveCount, next.CloudCreatedMark = &probe.ActiveCount, probe.CreatedMax
	res := &accountResult{marks: next}
	if decision.First {
		stats.firstSightings++
		return res, nil
	}

	created := 0
	if decision.CheckCreatedAfter != nil {
		res.changed = true
		limit := candidateCap()
		rows, err := repo.cloudRowsCreatedAfter(accountID, *decision.CheckCreatedAfter, limit+1)
		if err != nil {
			return nil, err
		}
		if len(rows) > limit {
			stats.capped++
			res.rebuild = true
			return res.finish(stats), nil
		}
		created = len(rows)
		creates, rebuild := d.createRefs(policy, tenantID, accountID, rows)
		if rebuild {
			res.rebuild = true
			return res.finish(stats), nil
		}
		res.refs = append(res.refs, creates...)
		stats.creates += len(creates)
	}

	switch DecideCountChange(*prev.CloudActiveCount, probe.ActiveCount, created) {
	case CountUnexplained:
		res.changed, res.rebuild = true, true
	case CountRemoved:
		res.changed = true
		ids, err := d.candidates.CloudDeleteCandidates(tenantID, accountID, []string{policy.Source()}, maxRefsPerAccount+1)
		if err != nil {
			return nil, err
		}
		deletes := deleteRefs(accountID, ids, "")
		stats.deletes += len(deletes)
		// Deletes first, so a resource replaced under the same key ends up active.
		res.refs = append(deletes, res.refs...)
	}
	return res.finish(stats), nil
}

// createRefs turns the rows created since the mark into upsert refs. rebuild is
// true when any row the graph reads is of a type that cannot be created on its
// own; rows the graph does not read are ignored.
func (d *Detector) createRefs(policy sources.CloudPatchPolicy, tenantID, accountID string, rows []sources.CloudResourceRow) (refs []kgqueue.KGResourceRef, rebuild bool) {
	req := &core.SourceBuildRequest{TenantID: tenantID, CloudAccountID: accountID}
	for i := range rows {
		row := &rows[i]
		if !policy.IncludeForGraph(row) {
			continue
		}
		if !d.createsEnabled {
			return nil, true
		}
		kind, builds := policy.Classify(row, req)
		if !builds {
			return nil, true
		}
		if spec := policy.Spec(kind); spec.AccountLevel || !spec.Creatable {
			return nil, true
		}
		refs = append(refs, kgqueue.KGResourceRef{
			AccountID:          accountID,
			ProviderResourceID: row.ID,
			NodeType:           string(kind.NodeType),
			Op:                 "upsert",
		})
	}
	return refs, false
}

// deleteRefs builds delete refs for resource ids.
func deleteRefs(accountID string, ids []string, nodeType core.NodeType) []kgqueue.KGResourceRef {
	refs := make([]kgqueue.KGResourceRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, kgqueue.KGResourceRef{
			AccountID:          accountID,
			ProviderResourceID: id,
			NodeType:           string(nodeType),
			Op:                 "delete",
		})
	}
	return refs
}
