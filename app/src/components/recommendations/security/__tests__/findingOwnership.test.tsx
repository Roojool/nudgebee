import { renderHook, waitFor } from '@testing-library/react';
import apiKubernetes from '@api1/kubernetes';
import { ownerTargetFor, useFindingOwnerTarget, type OwnerTarget } from '@components/recommendations/security/findingOwnership';
import type { SecurityFinding } from '@components/recommendations/security/securityFinding';

jest.mock('@api1/kubernetes', () => ({
  __esModule: true,
  default: { getK8sWorkload: jest.fn() },
}));

const getK8sWorkload = apiKubernetes.getK8sWorkload as jest.Mock;

const base = { id: 'f-1', accountId: 'acct-1', vulnId: 'CVE-2024-0001', severity: 'High', cweIds: [], references: [] };

const imageFinding = (overrides: Partial<SecurityFinding> = {}): SecurityFinding => ({
  ...base,
  source: 'image_scan',
  namespace: 'payments',
  workloadName: 'checkout',
  workloadType: 'Deployment',
  ...overrides,
});

const vmFinding = (overrides: Partial<SecurityFinding> = {}): SecurityFinding => ({
  ...base,
  id: 'f-2',
  source: 'vm',
  resourceId: 'vm-res-1',
  resourceName: 'build-agent-3',
  ...overrides,
});

beforeEach(() => {
  getK8sWorkload.mockReset();
});

describe('ownerTargetFor', () => {
  it('maps a VM finding straight onto its cloud resource', () => {
    expect(ownerTargetFor(vmFinding())).toEqual({ resource_id: 'vm-res-1', account_id: 'acct-1', resource_name: 'build-agent-3' });
  });

  it('has no target for a VM finding without a resource id', () => {
    expect(ownerTargetFor(vmFinding({ resourceId: undefined }))).toBeNull();
  });

  it('keys an image finding on the workload only once its id is known', () => {
    expect(ownerTargetFor(imageFinding())).toBeNull();
    expect(ownerTargetFor(imageFinding(), 'wl-res-1')).toEqual({
      resource_id: 'wl-res-1',
      account_id: 'acct-1',
      resource_name: 'checkout',
      resource_k8s_namespace: 'payments',
    });
  });
});

describe('useFindingOwnerTarget', () => {
  it('looks the workload up by exact name for an image finding', async () => {
    getK8sWorkload.mockResolvedValue({ data: { k8s_workloads: [{ cloud_resource_id: 'wl-res-1' }] } });
    const { result } = renderHook(() => useFindingOwnerTarget(imageFinding()));

    expect(result.current).toBeNull();
    await waitFor(() => expect(result.current?.resource_id).toBe('wl-res-1'));
    expect(getK8sWorkload).toHaveBeenCalledTimes(1);
    expect(getK8sWorkload).toHaveBeenCalledWith(
      1,
      0,
      { accountId: 'acct-1', namespaceName: 'payments', workloadName: 'checkout', exactNameMatch: true, workloadType: 'Deployment' },
      undefined,
      false
    );
    expect(result.current?.resource_k8s_namespace).toBe('payments');
  });

  it('stays without a target when the workload is not listed', async () => {
    getK8sWorkload.mockResolvedValue({ data: { k8s_workloads: [] } });
    const { result } = renderHook(() => useFindingOwnerTarget(imageFinding()));

    await waitFor(() => expect(getK8sWorkload).toHaveBeenCalledTimes(1));
    expect(result.current).toBeNull();
  });

  it('does not look anything up for a VM finding', () => {
    const { result } = renderHook(() => useFindingOwnerTarget(vmFinding()));

    expect(result.current?.resource_id).toBe('vm-res-1');
    expect(getK8sWorkload).not.toHaveBeenCalled();
  });

  it('never pairs the next finding with the previous workload id, not even for one render', async () => {
    getK8sWorkload.mockResolvedValueOnce({ data: { k8s_workloads: [{ cloud_resource_id: 'wl-res-1' }] } });
    const seen: (OwnerTarget | null)[] = [];
    const { result, rerender } = renderHook(
      ({ finding }) => {
        const target = useFindingOwnerTarget(finding);
        seen.push(target);
        return target;
      },
      { initialProps: { finding: imageFinding() } }
    );
    await waitFor(() => expect(result.current?.resource_id).toBe('wl-res-1'));

    getK8sWorkload.mockResolvedValueOnce({ data: { k8s_workloads: [{ cloud_resource_id: 'wl-res-2' }] } });
    rerender({ finding: imageFinding({ id: 'f-3', workloadName: 'ledger' }) });
    await waitFor(() => expect(result.current?.resource_id).toBe('wl-res-2'));

    expect(result.current?.resource_name).toBe('ledger');
    expect(seen.filter((t) => t?.resource_name === 'ledger' && t.resource_id !== 'wl-res-2')).toEqual([]);
  });

  it('drops a workload id when the drawer moves on to a VM finding', async () => {
    getK8sWorkload.mockResolvedValue({ data: { k8s_workloads: [{ cloud_resource_id: 'wl-res-1' }] } });
    const { result, rerender } = renderHook(({ finding }) => useFindingOwnerTarget(finding), { initialProps: { finding: imageFinding() } });
    await waitFor(() => expect(result.current?.resource_id).toBe('wl-res-1'));

    rerender({ finding: vmFinding() });
    expect(result.current?.resource_id).toBe('vm-res-1');
    expect(result.current?.resource_k8s_namespace).toBeUndefined();
  });
});
