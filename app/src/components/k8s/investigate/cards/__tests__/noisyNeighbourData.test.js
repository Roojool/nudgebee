import { buildMetricRows, formatOffset, parseNoisyNeighbours, partitionRows, primaryMetricFor } from '../noisyNeighbourData';

const cardEvent = (data) => ({
  aggregation_key: 'pod_oom_killer_enricher',
  evidences: [{ type: 'json', data: JSON.stringify({ name: 'noisy_neighbours', data }) }],
});

describe('primaryMetricFor', () => {
  // An OOM kill is a memory story; a throttled or never-ready pod is a CPU one.
  // Showing both at equal weight is what made the card 30 rows long.
  it('leads with memory for an OOM kill', () => {
    expect(primaryMetricFor('pod_oom_killer_enricher')).toBe('memory');
    expect(primaryMetricFor('report_crash_loop')).toBe('memory');
  });

  it('leads with CPU for the contention alerts, where memory looks fine', () => {
    expect(primaryMetricFor('CPUThrottlingHigh')).toBe('cpu');
    expect(primaryMetricFor('KubePodNotReady')).toBe('cpu');
    expect(primaryMetricFor('KubeContainerWaiting')).toBe('cpu');
  });

  it('falls back to memory for anything unrecognised', () => {
    expect(primaryMetricFor(undefined)).toBe('memory');
  });
});

describe('parseNoisyNeighbours', () => {
  it('returns null when the event carries no noisy-neighbours card', () => {
    expect(parseNoisyNeighbours({ evidences: [{ type: 'json', data: JSON.stringify({ name: 'something_else' }) }] })).toBeNull();
    expect(parseNoisyNeighbours(undefined)).toBeNull();
  });

  // A provider that cannot measure CPU omits the keys entirely. Absent must not
  // collapse into "0 cores in use", which is a different and misleading claim.
  it('distinguishes CPU that was not measured from CPU that was zero', () => {
    expect(parseNoisyNeighbours(cardEvent({ node_name: 'n1' })).cpu).toBeNull();
    expect(parseNoisyNeighbours(cardEvent({ node_name: 'n1', cpu_allocatable: 4, cpu_used: 0 })).cpu).toEqual({ used: 0, allocatable: 4 });
  });

  it('reads the pod count, the subject, and the co-killed list', () => {
    const card = parseNoisyNeighbours(
      cardEvent({
        node_name: 'n1',
        pods_on_node: 63,
        subject_pod: 'api-1',
        subject_measured: false,
        co_killed: [{ pod_name: 'vmsingle', namespace: 'victoria', offset_seconds: -2 }],
      })
    );
    expect(card.podsOnNode).toBe(63);
    expect(card.subjectPod).toBe('api-1');
    expect(card.subjectMeasured).toBe(false);
    expect(card.coKilled).toHaveLength(1);
  });
});

describe('buildMetricRows', () => {
  const entries = [
    { pod_name: 'hog', namespace: 'demo', name: 'app', memory_used: 800, memory_requested: 100, memory_limit: 1000 },
    { pod_name: 'api-1', namespace: 'shop', name: 'app', memory_used: 200, is_subject: true },
  ];

  it('flags the subject so the card can pin the row the reader came for', () => {
    const rows = buildMetricRows({ entries, coKilled: [], metric: 'memory', allocatable: 2000 });
    expect(rows.find((r) => r.podName === 'api-1').isSubject).toBe(true);
    expect(rows.find((r) => r.podName === 'hog').isSubject).toBe(false);
  });

  // The case the whole change is about: a pod killed on the same node moments
  // earlier has no metric series to rank, so it only reaches the reader if the
  // card adds it from the events data.
  it('adds a co-killed pod that never appeared in the metrics at all', () => {
    const rows = buildMetricRows({
      entries,
      coKilled: [{ pod_name: 'vmsingle', namespace: 'victoria', offset_seconds: -2 }],
      metric: 'memory',
      allocatable: 2000,
    });
    const coKilled = rows.find((r) => r.podName === 'vmsingle');
    expect(coKilled).toBeDefined();
    expect(coKilled.coKilledOffset).toBe(-2);
    // Never rendered as a zero reading — it was not sampled.
    expect(coKilled.measured).toBe(false);
    expect(coKilled.value).toBeUndefined();
  });

  it('tags a co-killed pod that IS in the metrics rather than duplicating it', () => {
    const rows = buildMetricRows({
      entries,
      coKilled: [{ pod_name: 'hog', namespace: 'demo', offset_seconds: 30 }],
      metric: 'memory',
      allocatable: 2000,
    });
    expect(rows.filter((r) => r.podName === 'hog')).toHaveLength(1);
    expect(rows.find((r) => r.podName === 'hog').coKilledOffset).toBe(30);
  });

  // The card shows warnings[0] as a chip and puts the rest in the tooltip, so the
  // order is load-bearing: most actionable first. "unbounded" is what explains an
  // OOM kill — nothing capped the container — and outranks a missing request.
  it('turns the per-row sentences into short chips, most actionable first', () => {
    const rows = buildMetricRows({
      entries: [{ pod_name: 'p', namespace: 'ns', name: 'c', memory_used: 200, memory_requested: 100 }],
      coKilled: [],
      metric: 'memory',
      allocatable: 1000,
    });
    expect(rows[0].warnings.map((w) => w.label)).toEqual(['100% over request', 'unbounded']);
    expect(rows[0].warnings[1].detail).toContain('no memory limit');
  });

  it('sorts unmeasured rows last instead of treating them as zero', () => {
    const rows = buildMetricRows({
      entries: [{ pod_name: 'small', namespace: 'ns', name: 'c', memory_used: 1 }],
      coKilled: [{ pod_name: 'gone', namespace: 'ns', offset_seconds: -1 }],
      metric: 'memory',
      allocatable: 1000,
    });
    expect(rows.map((r) => r.podName)).toEqual(['small', 'gone']);
  });
});

describe('partitionRows', () => {
  const row = (podName, extra = {}) => ({ podName, isSubject: false, coKilledOffset: null, value: 1, ...extra });

  // The subject and anything the node also killed are the reason to open the
  // card, so they are never the rows hidden behind "Show all".
  it('always shows the subject and the co-killed pods, whatever their ranking', () => {
    const rows = [row('a'), row('b'), row('c'), row('d'), row('quiet-subject', { isSubject: true }), row('quiet-victim', { coKilledOffset: -2 })];

    const { visible, hidden } = partitionRows(rows, 3);

    expect(visible.map((r) => r.podName)).toEqual(['quiet-subject', 'quiet-victim', 'a', 'b', 'c']);
    expect(hidden.map((r) => r.podName)).toEqual(['d']);
  });

  it('hides nothing when the list is already short', () => {
    expect(partitionRows([row('a'), row('b')], 3).hidden).toEqual([]);
  });
});

describe('formatOffset', () => {
  // "2s before" is what makes two separate rows read as one machine failing.
  it.each([
    [-2, '2s before'],
    [45, '45s after'],
    [-120, '2m before'],
    [65, '1m 5s after'],
    [0, 'at the same moment'],
  ])('renders %i as %s', (seconds, expected) => {
    expect(formatOffset(seconds)).toBe(expected);
  });
});
