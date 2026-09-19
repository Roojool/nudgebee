package changedetect

import (
	"nudgebee/services/knowledge_graph/core"
	kgqueue "nudgebee/services/knowledge_graph/queue"
)

// probeK8sAccount checks one Kubernetes cluster account.
//
//   - Workloads or nodes created (or come back): the account rebuilds.
//   - Workloads or nodes removed: they are named and get delete refs.
//   - Workloads rolled out: upsert refs.
func (d *Detector) probeK8sAccount(repo *repository, tenantID, accountID string, prev, next *Marks, stats *runStats) (*accountResult, error) {
	kinds := d.k8s.DetectedWorkloadKinds()
	probe, err := repo.probeK8s(tenantID, accountID, kinds, rolloutSettle)
	if err != nil {
		return nil, err
	}
	decision := DecideK8s(prev, probe)
	next.K8sActiveCount, next.K8sCreatedMark = &probe.WorkloadCount, probe.CreatedMax
	next.NodeCount, next.NodeCreatedMark = &probe.NodeCount, probe.NodeCreated
	next.K8sDeployedMark = laterMark(next.K8sDeployedMark, probe.DeployedMax)
	res := &accountResult{marks: next}
	if decision.First {
		stats.firstSightings++
		return res, nil
	}
	if decision.Rebuild {
		res.rebuild = true
		return res.finish(stats), nil
	}

	limit := maxRefsPerAccount + 1
	if decision.WorkloadsRemoved {
		res.changed = true
		ids, err := d.candidates.K8sWorkloadDeleteCandidates(tenantID, accountID, d.k8s.WorkloadSpecificTypes(kinds), limit)
		if err != nil {
			return nil, err
		}
		deletes := deleteRefs(accountID, ids, core.NodeTypeWorkload)
		stats.deletes += len(deletes)
		res.refs = append(res.refs, deletes...)
	}
	if decision.NodesRemoved {
		res.changed = true
		names, err := d.candidates.K8sNodeDeleteCandidates(tenantID, accountID, d.k8s.NodeSpecificType(), limit)
		if err != nil {
			return nil, err
		}
		deletes := deleteRefs(accountID, names, core.NodeTypeNode)
		stats.deletes += len(deletes)
		res.refs = append(res.refs, deletes...)
	}
	if decision.RefsDeployedAfter != nil {
		res.changed = true
		ids, err := repo.deployedWorkloads(tenantID, accountID, kinds, *decision.RefsDeployedAfter, *probe.DeployedMax, limit)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			res.refs = append(res.refs, kgqueue.KGResourceRef{
				AccountID:          accountID,
				ProviderResourceID: id,
				NodeType:           string(core.NodeTypeWorkload),
				Op:                 "upsert",
			})
		}
	}
	return res.finish(stats), nil
}
