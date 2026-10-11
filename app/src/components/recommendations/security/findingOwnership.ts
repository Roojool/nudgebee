/**
 * The resource a security finding is about, in the shape the Optimise
 * ownership card reads (a recommendation row: resource_id, account_id,
 * resource_k8s_namespace, resource_name).
 *
 * A VM finding already carries the machine's cloud resource id. An image-scan
 * finding is keyed by image and located by namespace/workload, so the workload's
 * cloud resource id — what the ownership resolver is keyed on — has to be looked
 * up from the workload list first.
 */
import { useEffect, useState } from 'react';
import apiKubernetes from '@api1/kubernetes';
import { useLatestRequest } from '@components/vm/common';
import type { SecurityFinding } from './securityFinding';

export interface OwnerTarget {
  resource_id: string;
  account_id: string;
  resource_name?: string;
  resource_k8s_namespace?: string;
}

/** The target for a finding, given the workload id when one was looked up. */
export const ownerTargetFor = (finding: SecurityFinding | null, workloadId?: string | null): OwnerTarget | null => {
  if (!finding?.accountId) return null;
  if (finding.source === 'vm') {
    return finding.resourceId ? { resource_id: finding.resourceId, account_id: finding.accountId, resource_name: finding.resourceName } : null;
  }
  if (!workloadId || !finding.namespace) return null;
  return {
    resource_id: workloadId,
    account_id: finding.accountId,
    resource_name: finding.workloadName,
    resource_k8s_namespace: finding.namespace,
  };
};

export interface WorkloadRef {
  accountId: string;
  namespace?: string;
  workloadName?: string;
  workloadType?: string;
}

/** The cloud resource id of the workload an image-scan finding sits in, or null when it is not listed. */
export const lookupWorkloadId = async ({ accountId, namespace, workloadName, workloadType }: WorkloadRef): Promise<string | null> => {
  if (!accountId || !namespace || !workloadName) return null;
  const res = await apiKubernetes.getK8sWorkload(
    1,
    0,
    // The list builder reads its filters from these spellings only.
    { accountId, namespaceName: namespace, workloadName, exactNameMatch: true, ...(workloadType ? { workloadType } : {}) },
    undefined,
    false
  );
  return res?.data?.k8s_workloads?.[0]?.cloud_resource_id || null;
};

export function useFindingOwnerTarget(finding: SecurityFinding | null): OwnerTarget | null {
  const needsLookup = finding?.source === 'image_scan';
  const accountId = finding?.accountId || '';
  const namespace = finding?.namespace;
  const workloadName = finding?.workloadName;
  const workloadType = finding?.workloadType;
  // Identity of the lookup a stored id belongs to. The drawer swaps findings
  // without remounting, so the id is only used while it matches the current
  // finding; a swap never pairs the new finding with the previous workload.
  const lookupKey = needsLookup ? [accountId, namespace, workloadName, workloadType].join('\u0000') : '';
  const [resolved, setResolved] = useState<{ key: string; id: string | null } | null>(null);
  const beginRequest = useLatestRequest();

  useEffect(() => {
    // Retire any lookup in flight, including when the new finding needs none.
    const isLatest = beginRequest();
    if (!lookupKey) return;
    lookupWorkloadId({ accountId, namespace, workloadName, workloadType })
      .then((id) => {
        if (isLatest()) setResolved({ key: lookupKey, id });
      })
      .catch((error) => {
        console.error('Failed to resolve the workload for a finding:', error);
      });
  }, [lookupKey, accountId, namespace, workloadName, workloadType, beginRequest]);

  const workloadId = resolved && resolved.key === lookupKey ? resolved.id : null;
  return ownerTargetFor(finding, workloadId);
}
