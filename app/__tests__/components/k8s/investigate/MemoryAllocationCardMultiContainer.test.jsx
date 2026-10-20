import React from 'react';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom';

const mockGetK8sWorkload = jest.fn();

// jsdom has no canvas; stand the chart in with its data so the sparkline path
// is exercised instead of failing inside its error boundary.
jest.mock('chart.js', () => ({
  Chart: { register: jest.fn() },
  CategoryScale: jest.fn(),
  LinearScale: jest.fn(),
  PointElement: jest.fn(),
  LineElement: jest.fn(),
  Filler: jest.fn(),
  Title: jest.fn(),
  Tooltip: jest.fn(),
  Legend: jest.fn(),
  Colors: jest.fn(),
}));
jest.mock('react-chartjs-2', () => ({
  Line: ({ data, id }) => <div data-testid={`line-chart-${id}`} data-points={data.datasets[0]?.data?.length ?? 0} />,
}));
jest.mock('@api1/tickets', () => ({
  __esModule: true,
  default: { listTicketConfigsForCreate: jest.fn(() => Promise.resolve({ data: [] })) },
}));
jest.mock('@api1/recommendation', () => ({ __esModule: true, default: { applyRecommendation: jest.fn() } }));
jest.mock('@api1/kubernetes', () => ({
  __esModule: true,
  default: {
    getK8sWorkload: (...args) => mockGetK8sWorkload(...args),
    getResourceAttributes: jest.fn(() => Promise.resolve([])),
  },
}));

import MemoryAllocationCard from '@components/k8s/investigate/cards/MemoryAllocationCard';

// #38638, event 30dc2b38: the Prometheus StatefulSet pod was OOMKilled at its 8Gi
// limit. It is also the metrics backend, so every query for its own container was
// refused and the evidence holds only the config-reloader sidecar (peak
// 20,262,912 bytes, nothing set). The form used to open on the sidecar, show
// "Not set" and propose 21.26 MB for the pod.
const sidecar = {
  container: 'config-reloader',
  limits: { cpu: 0, memory: 0 },
  namespace: 'nudgebee-agent',
  pod: 'prometheus-nudgebee-prometheus-kube-p-prometheus',
  requests: { cpu: 0, memory: 0 },
};
const evidences = [
  {
    type: 'table',
    data: {
      table_name: '*Pod and Node OOMKilled data*',
      rows: [
        ['Pod', 'prometheus-nudgebee-prometheus-kube-p-prometheus-0'],
        ['Container name', 'prometheus'],
        ['Container memory', '2048MB request, 8192MB limit'],
      ],
    },
  },
  { type: 'json', data: JSON.stringify({ name: 'pod_metric', data: [], resource_type: 'cpu' }) },
  {
    type: 'json',
    data: JSON.stringify({
      name: 'pod_metric',
      data: [
        {
          metric: sidecar,
          timestamps: [1789609928, 1789609930, 1789609932, 1789609934, 1789609936],
          values: ['18000000', '19000000', '20262912', '19500000', '19000000'],
        },
      ],
      resource_type: 'memory',
    }),
  },
];

const event = {
  id: '30dc2b38-6e3c-4e4d-aacd-ffe533a38456',
  cloud_account_id: '21a3b9a9-8d4e-4a8f-9744-dcd61d828344',
  subject_type: 'pod',
  // Two parts: the old serviceKeys[2] read found no workload here and guessed
  // a Deployment named "prometheus-nudgebee-prometheus-kube-p".
  service_key: 'nudgebee-agent/prometheus-nudgebee-prometheus-kube-p-prometheus',
  subject_namespace: 'nudgebee-agent',
  subject_name: 'prometheus-nudgebee-prometheus-kube-p-prometheus-0',
  subject_owner: 'prometheus-nudgebee-prometheus-kube-p-prometheus',
  subject_owner_kind: 'statefulset',
  starts_at: '2026-09-17T02:02:08.000',
  evidences,
};

const statefulSet = {
  data: {
    k8s_workloads: [
      {
        cloud_resource_id: 'res-1',
        meta: {
          config: {
            annotations: {},
            labels: {},
            containers: [
              { name: 'prometheus', resources: { requests: { cpu: '200m', memory: '2Gi' }, limits: { memory: '8Gi' } } },
              { name: 'config-reloader' },
            ],
          },
        },
      },
    ],
  },
};

const inputValues = () => Object.fromEntries(Array.from(document.querySelectorAll('input')).map((i) => [i.name, i.value]));

const openTuneModal = async () => {
  const card = new MemoryAllocationCard();
  await card.canRenderContent(evidences, event);
  const Resolve = card.getResolveComponent();
  render(<Resolve open onCloseComponent={jest.fn()} />);
  await waitFor(() => expect(document.querySelectorAll('input').length).toBeGreaterThan(0));
};

describe('Tune Resource Configuration — multi-container pod', () => {
  beforeEach(() => {
    mockGetK8sWorkload.mockReset();
    mockGetK8sWorkload.mockResolvedValue(statefulSet);
  });

  it('looks up the owning StatefulSet by exact name', async () => {
    await openTuneModal();
    expect(mockGetK8sWorkload).toHaveBeenCalledWith(
      1,
      0,
      expect.objectContaining({
        workloadName: 'prometheus-nudgebee-prometheus-kube-p-prometheus',
        workloadType: 'StatefulSet',
        exactNameMatch: true,
      })
    );
  });

  it('opens on the OOMKilled container with its real allocation', async () => {
    await openTuneModal();

    const prometheus = screen.getByRole('button', { name: /prometheus/ });
    expect(prometheus).toHaveAttribute('aria-pressed', 'true');
    expect(prometheus).toHaveTextContent('Out of memory');
    expect(screen.getByRole('button', { name: 'config-reloader' })).toHaveAttribute('aria-pressed', 'false');
    await waitFor(() => expect(screen.getByText('2048')).toBeInTheDocument());
    expect(screen.getByText('8192')).toBeInTheDocument();
    // 2Gi and 8Gi plus the default 10% buffer — never the sidecar's 21.26.
    await waitFor(() => expect(inputValues()).toMatchObject({ memoryRequest: '2252.8', memoryLimit: '9011.2' }));
  });

  it('switches to the sidecar without leaving the form', async () => {
    await openTuneModal();
    await waitFor(() => expect(inputValues()).toMatchObject({ memoryRequest: '2252.8' }));

    fireEvent.click(screen.getByRole('button', { name: 'config-reloader' }));

    // Its own peak (20,262,912 bytes) + 10%, and nothing set as Current.
    await waitFor(() => expect(inputValues()).toMatchObject({ memoryRequest: '21.26', memoryLimit: '21.26' }));
    expect(screen.queryByText('2048')).not.toBeInTheDocument();
    expect(screen.getAllByText('Not set').length).toBeGreaterThan(0);
  });

  it('lists the killed container on the evidence card even with no samples', async () => {
    const card = new MemoryAllocationCard();
    await card.canRenderContent(evidences, event);
    const components = card.getContentComponents();
    render(
      <>
        {components.map((Component, i) => (
          <Component key={i} />
        ))}
      </>
    );

    expect(screen.getByText('config-reloader')).toBeInTheDocument();
    expect(screen.getByText('prometheus')).toBeInTheDocument();
    expect(screen.getByText('No usage samples were recorded for this container around the event.')).toBeInTheDocument();
    // The allocation comes from the OOMKilled table: 2048MB request, 8192MB limit.
    const killed = card.podMemoryAllocationItem.find((item) => item.container === 'prometheus');
    expect(killed).toMatchObject({ request: 2048 * 1024 * 1024, limits: 8192 * 1024 * 1024 });
  });

  it('shows what the pod used next to the proposal when the container has no series', async () => {
    const podUsage = {
      type: 'json',
      data: JSON.stringify({
        name: 'pod_node_metrics',
        resource_type: 'memory',
        data: {
          memory_usage: {
            series_list_result: [
              { metric: {}, timestamps: [1789606329, 1789606389, 1789606449], values: ['5000000000', '8485283840', '6000000000'] },
            ],
          },
          memory_limit: { series_list_result: [{ metric: {}, timestamps: [1789606329], values: ['8589934592'] }] },
        },
      }),
    };
    const card = new MemoryAllocationCard();
    const withPod = [...evidences, podUsage];
    await card.canRenderContent(withPod, { ...event, evidences: withPod });
    const Resolve = card.getResolveComponent();
    render(<Resolve open onCloseComponent={jest.fn()} />);
    await waitFor(() => expect(inputValues()).toMatchObject({ memoryLimit: '9011.2' }));

    expect(screen.getByTestId('line-chart-tune-memory-usage')).toHaveAttribute('data-points', '3');
    const facts = screen.getByTestId('memory-usage-facts');
    expect(facts).toHaveTextContent('Highest 8,092 MB');
    expect(facts).toHaveTextContent('Whole pod, all containers');
    // 9,011.2 − 8,092.1 MB of headroom, and the reservation well below real use.
    const check = screen.getByTestId('memory-headroom-check');
    expect(check).toHaveTextContent('Limit is 919 MB above the most the pod has used (8,092 MB).');
    expect(check).toHaveTextContent('Reserves 2,253 MB while the pod has used up to 8,092 MB.');

    // Typing a limit below the peak flips the check.
    fireEvent.change(document.querySelector('input[name="memoryLimit"]'), { target: { value: '4000' } });
    await waitFor(() => expect(screen.getByTestId('memory-headroom-check')).toHaveTextContent('it would run out of memory again'));
  });

  it('lists spec containers the evidence never mentioned', async () => {
    const withThird = JSON.parse(JSON.stringify(statefulSet));
    withThird.data.k8s_workloads[0].meta.config.containers.push({
      name: 'thanos-sidecar',
      resources: { requests: { memory: '256Mi' }, limits: { memory: '512Mi' } },
    });
    mockGetK8sWorkload.mockResolvedValue(withThird);
    await openTuneModal();

    const thanos = await screen.findByRole('button', { name: 'thanos-sidecar' });
    fireEvent.click(thanos);

    // No metrics for it, so Current and the proposal come from its spec.
    await waitFor(() => expect(inputValues()).toMatchObject({ memoryRequest: '281.6', memoryLimit: '563.2' }));
    expect(screen.getByText('256')).toBeInTheDocument();
    expect(screen.getByText('512')).toBeInTheDocument();
  });
});
