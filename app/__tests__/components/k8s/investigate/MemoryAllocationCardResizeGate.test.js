import MemoryAllocationCard from '@components/k8s/investigate/cards/MemoryAllocationCard';

// The card renders on every pod event that has metrics, but a resize is only a
// fix when resources were the problem. On dev, replica-mismatch, pod-not-ready
// and API-failure events all offered "Adjust resource requests and limits".

const MIB = 1024 * 1024;

const podMetric = (resourceType, element) => ({
  type: 'json',
  data: JSON.stringify({ name: 'pod_metric', resource_type: resourceType, data: [element] }),
});

const memoryElement = (peakMib, limitMib) => ({
  metric: { container: 'app', pod: 'app', namespace: 'ns', requests: { memory: 64 * MIB, cpu: 0.1 }, limits: { memory: limitMib * MIB, cpu: 1 } },
  timestamps: [1, 2, 3, 4, 5],
  values: [peakMib * 0.5, peakMib * 0.8, peakMib, peakMib * 0.9, peakMib * 0.7].map((v) => String(v * MIB)),
});

const cpuElement = (peakCores, limitCores) => ({
  metric: { container: 'app', pod: 'app', namespace: 'ns', requests: { memory: 64 * MIB, cpu: 0.1 }, limits: { memory: 256 * MIB, cpu: limitCores } },
  timestamps: [1, 2, 3],
  values: [String(peakCores / 2), String(peakCores), String(peakCores / 3)],
});

const offersResize = async (evidences) => {
  const card = new MemoryAllocationCard();
  await card.canRenderContent(evidences, { id: 'e', subject_type: 'pod', subject_name: 'app-1-2', starts_at: '2026-09-17T00:00:00', evidences });
  return card.resolveButton;
};

describe('MemoryAllocationCard — when resize is offered', () => {
  it('not for an event whose memory and CPU stayed well under their limits', async () => {
    expect(await offersResize([podMetric('memory', memoryElement(100, 256)), podMetric('cpu', cpuElement(0.2, 1))])).toBe(false);
  });

  it('when memory reached 85% of the limit', async () => {
    expect(await offersResize([podMetric('memory', memoryElement(220, 256))])).toBe(true);
  });

  it('when CPU reached 85% of the limit', async () => {
    expect(await offersResize([podMetric('cpu', cpuElement(0.9, 1))])).toBe(true);
  });

  it('when a container was killed for running out of memory, whatever the series say', async () => {
    const oomTable = {
      type: 'table',
      data: {
        table_name: 'Pod and Node OOMKilled data',
        rows: [
          ['Container name', 'app'],
          ['Container memory', '64Mi request, 256Mi limit'],
        ],
      },
    };
    expect(await offersResize([oomTable, podMetric('memory', memoryElement(3, 256))])).toBe(true);
  });

  it('not when the limit is unset — that is an Optimize recommendation, not this event’s fix', async () => {
    const unlimited = memoryElement(500, 0);
    expect(await offersResize([podMetric('memory', unlimited)])).toBe(false);
  });
});
