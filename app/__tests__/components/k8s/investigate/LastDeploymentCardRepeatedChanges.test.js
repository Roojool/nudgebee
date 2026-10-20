import LastDeploymentCard, { foldRepeatedChanges, canRevertDiff } from '@components/k8s/investigate/cards/LastDeploymentCard';

// prometheus-operator rewrites its rulefiles ConfigMap on every rule change. Five rewrites before an
// OOM rendered five identical evidence cards and five "Revert" rows that all sent the same request —
// one that wrote the ConfigMap's old values onto the Prometheus StatefulSet.
const RULEFILES = 'configmap/nudgebee-agent/prometheus-rulefiles-0.yaml';
const diff = (resourceName, startAt, info) => ({
  type: 'diff',
  start_at: startAt,
  data: { resource_name: resourceName, old: 'a', new: 'b', updated_values: [{ path: 'data.rules', old: 'a', new: 'b' }] },
  ...(info && { additional_info: info }),
});

describe('foldRepeatedChanges', () => {
  it('keeps the first (newest) block of an object and skips the repeats', () => {
    const evidences = [
      { type: 'table' },
      diff(RULEFILES, 't5'),
      diff(RULEFILES, 't4'),
      diff('statefulset/ns/prom.yaml', 't3'),
      diff(RULEFILES, 't2'),
    ];
    const { skip, changes } = foldRepeatedChanges(evidences);
    expect([...skip]).toEqual([2, 4]);
    // Earlier edits keep their own diff, so each can still be opened.
    expect(changes.get(1).map((c) => c.start_at)).toEqual(['t5', 't4', 't2']);
    expect(changes.get(1)[1].diff).toBe(evidences[2].data);
    expect(changes.get(3).map((c) => c.start_at)).toEqual(['t3']);
  });

  it('never folds blocks that do not name their object', () => {
    const { skip } = foldRepeatedChanges([diff('', 't1'), diff(undefined, 't2')]);
    expect(skip.size).toBe(0);
  });

  it('leaves the deployment history evidence alone', () => {
    const history = { type: 'json', additional_info: { action_name: 'deployment_history' }, data: {} };
    expect(foldRepeatedChanges([history, history]).skip.size).toBe(0);
  });
});

describe('canRevertDiff', () => {
  const event = (evidences) => ({ service_key: 'nudgebee-agent/prometheus', evidences });

  it('does not offer to revert a ConfigMap edit onto the workload', () => {
    const cm = diff(RULEFILES, 't1', { change_event_id: 'chg-1' });
    expect(canRevertDiff(cm, event([cm]))).toBe(false);
  });

  it('offers a named change to the alert workload', () => {
    const own = diff('statefulset/nudgebee-agent/prometheus.yaml', 't1', { change_event_id: 'chg-1' });
    expect(canRevertDiff(own, event([own, diff(RULEFILES, 't0')]))).toBe(true);
  });

  it('does not offer an unnamed change when the alert carries several', () => {
    const own = diff('statefulset/nudgebee-agent/prometheus.yaml', 't1');
    expect(canRevertDiff(own, event([own, diff(RULEFILES, 't0')]))).toBe(false);
    expect(canRevertDiff(own, event([own]))).toBe(true);
  });

  it('does not offer a change to a different workload', () => {
    const other = diff('deployment/nudgebee-agent/grafana.yaml', 't1', { change_event_id: 'chg-1' });
    expect(canRevertDiff(other, event([other]))).toBe(false);
  });
});

describe('LastDeploymentCard with folded repeats', () => {
  it('says how many times the object changed and sends the change id with the revert', async () => {
    const block = diff(RULEFILES, 't5');
    block.additional_info = { title: 'ConfigMap prometheus-rulefiles-0 changed · 06:28 UTC' };
    const earlier = diff(RULEFILES, 't4');
    const folded = [
      { start_at: 't5', diff: block.data },
      { start_at: 't4', diff: earlier.data },
    ];
    const card = new LastDeploymentCard(block, { service_key: 'nudgebee-agent/prometheus', evidences: [block, earlier] }, 1, folded);
    expect(await card.canRenderContent()).toBe(true);
    // canRenderContent runs more than once per card; the count must not be appended twice.
    await card.canRenderContent();
    expect(card.text).toBe('ConfigMap prometheus-rulefiles-0 changed · 06:28 UTC · 2 times');
    expect(card.changes.map((c) => c.diff)).toEqual([block.data, earlier.data]);
    expect(card.resolveButton).toBe(false);
    expect(card.getResolveComponent()).toBeNull();
  });

  it('does not repeat a count the collector already put in the title', async () => {
    const block = diff(RULEFILES, 't5', {
      title: 'ConfigMap prometheus-rulefiles-0 changed 2 times · latest 06:28 UTC',
      change_count: 2,
      change_event_id: 'chg-5',
      changes: [
        { change_event_id: 'chg-5', start_at: 't5' },
        { change_event_id: 'chg-4', start_at: 't4' },
      ],
    });
    const card = new LastDeploymentCard(block, { evidences: [block] }, 1);
    await card.canRenderContent();
    expect(card.text).toBe('ConfigMap prometheus-rulefiles-0 changed 2 times · latest 06:28 UTC');
    // The newest diff is on the page; older edits are fetched by their own id when opened.
    expect(card.changes).toEqual([
      { startAt: 't5', eventId: 'chg-5', diff: block.data },
      { startAt: 't4', eventId: 'chg-4' },
    ]);
  });
});
