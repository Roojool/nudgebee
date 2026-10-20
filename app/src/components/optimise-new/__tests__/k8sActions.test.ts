import { buildK8sApplyData, k8sActionFor, parseSizeGi } from '../k8sActions';

const volumeRec = {
  id: 'rec-pv',
  account_id: 'acct-1',
  category: 'RightSizing',
  rule_name: 'unused_pvc',
  recommendation: {
    metadata: { name: 'pvc-0a1b' },
    spec: { capacity: { storage: '20Gi' }, claimRef: { namespace: 'payments', name: 'data-ledger-0' } },
  },
};

const workloadRec = {
  id: 'rec-wl',
  account_id: 'acct-1',
  category: 'RightSizing',
  rule_name: 'abandoned_resource',
  resource_name: 'ledger-worker',
  resource_k8s_namespace: 'payments',
  cloud_resourse: { name: 'ledger-worker-7d9f', meta: { controller: 'ledger-worker', controllerKind: 'Deployment', namespace: 'payments' } },
};

const resizeRec = {
  id: 'rec-resize',
  account_id: 'acct-1',
  category: 'RightSizing',
  rule_name: 'pv_rightsize',
  recommendation: JSON.stringify({
    spec: { capacity: { storage: '100Gi' }, claimRef: { namespace: 'payments', name: 'data-ledger-0' } },
    recommendation: { recommend_size: 40, capacity: 107374182400 },
  }),
};

describe('k8sActionFor', () => {
  it('describes each of the three agent fixes from its own payload', () => {
    expect(k8sActionFor(volumeRec)).toMatchObject({ rule: 'unused_pvc', label: 'Reclaim Volume', input: 'typed-name', subject: 'pvc-0a1b' });
    expect(k8sActionFor(workloadRec)).toMatchObject({
      rule: 'abandoned_resource',
      label: 'Scale Down Workload',
      input: 'typed-name',
      subject: 'ledger-worker',
    });
    expect(k8sActionFor(resizeRec)).toMatchObject({
      rule: 'pv_rightsize',
      label: 'Resize Volume',
      input: 'size',
      subject: 'data-ledger-0',
      defaultSize: 40,
    });
  });

  it('shows the facts the cluster page shows', () => {
    expect(k8sActionFor(volumeRec)?.facts).toEqual([
      { label: 'Volume', value: 'pvc-0a1b', copyable: true },
      { label: 'Size', value: '20Gi' },
      { label: 'Last claim', value: 'payments/data-ledger-0' },
    ]);
    expect(k8sActionFor(resizeRec)?.facts).toEqual([
      { label: 'Claim', value: 'payments/data-ledger-0', copyable: true },
      { label: 'Current size', value: '100Gi' },
      { label: 'Recommended', value: '40Gi' },
    ]);
  });

  it('offers nothing for rules it does not serve, or a payload it cannot act on', () => {
    expect(k8sActionFor({ ...volumeRec, rule_name: 'pod_right_sizing' })).toBeNull();
    expect(k8sActionFor({ ...volumeRec, category: 'Configuration' })).toBeNull();
    expect(k8sActionFor({ ...volumeRec, recommendation: {} })).toBeNull();
    expect(
      k8sActionFor({ ...resizeRec, recommendation: { spec: { capacity: { storage: '10Gi' } }, recommendation: { recommend_size: 4 } } })
    ).toBeNull();
    expect(k8sActionFor(null)).toBeNull();
  });

  it('still offers the resize when the finding names no size, with the field left empty', () => {
    const action = k8sActionFor({
      ...resizeRec,
      recommendation: { spec: { claimRef: { name: 'data-0', namespace: 'payments' } }, recommendation: {} },
    });
    expect(action).toMatchObject({ rule: 'pv_rightsize', input: 'size', subject: 'data-0' });
    expect(action?.defaultSize).toBeUndefined();
    expect(action?.facts.map((f) => f.label)).toEqual(['Claim']);
  });
});

describe('buildK8sApplyData', () => {
  it('withholds a removal until the exact name is typed', () => {
    const action = k8sActionFor(volumeRec)!;
    expect(buildK8sApplyData(action, volumeRec, { typedName: '' })).toBeNull();
    expect(buildK8sApplyData(action, volumeRec, { typedName: 'pvc-0a1' })).toBeNull();
    expect(buildK8sApplyData(action, volumeRec, { typedName: 'pvc-0a1b' })).toEqual({ name: 'pvc-0a1b' });
  });

  it('records the scale-down as what it is: the workload to zero replicas', () => {
    const action = k8sActionFor(workloadRec)!;
    expect(buildK8sApplyData(action, workloadRec, { typedName: 'ledger-worker' })).toEqual({
      name: 'ledger-worker',
      namespace: 'payments',
      kind: 'Deployment',
      replica_count: 0,
    });
  });

  it('sends the size the reader settled on, under the key the backend reads', () => {
    const action = k8sActionFor(resizeRec)!;
    expect(buildK8sApplyData(action, resizeRec, { sizeGi: null })).toBeNull();
    expect(buildK8sApplyData(action, resizeRec, { sizeGi: 50 })).toEqual({ name: 'data-ledger-0', namespace: 'payments', recommendedSize: 50 });
  });
});

describe('parseSizeGi', () => {
  it('accepts only a positive number', () => {
    expect(parseSizeGi('40')).toBe(40);
    expect(parseSizeGi(' 12.5 ')).toBe(12.5);
    expect(parseSizeGi('0')).toBeNull();
    expect(parseSizeGi('-3')).toBeNull();
    expect(parseSizeGi('lots')).toBeNull();
    expect(parseSizeGi('')).toBeNull();
  });
});
