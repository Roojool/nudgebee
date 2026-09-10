import React from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom';

const mockListTicketConfigsForCreate = jest.fn();
const mockGetK8sWorkload = jest.fn();

jest.mock('@api1/tickets', () => ({
  __esModule: true,
  default: {
    listTicketConfigsForCreate: (...args) => mockListTicketConfigsForCreate(...args),
  },
}));

jest.mock('@api1/recommendation', () => ({
  __esModule: true,
  default: {
    applyRecommendation: jest.fn(),
  },
}));

jest.mock('@api1/kubernetes', () => ({
  __esModule: true,
  default: {
    getK8sWorkload: (...args) => mockGetK8sWorkload(...args),
    getResourceAttributes: jest.fn(() => Promise.resolve([])),
  },
}));

import KubernetesRightSizingUpdateForm from '@components/recommendations/KubernetesRightSizingUpdateForm';

// A workload whose container spec carries the requests/limits the event evidence
// path failed to report (kube_pod_container_resource_* series missing).
const workloadWithSpec = {
  data: {
    k8s_workloads: [
      {
        cloud_resource_id: 'res-1',
        meta: {
          config: {
            annotations: {},
            labels: {},
            containers: [
              {
                name: 'relay-server',
                resources: { requests: { cpu: '100m', memory: '128Mi' }, limits: { memory: '307Mi' } },
              },
            ],
          },
        },
      },
    ],
  },
};

const baseData = {
  id: 'event-1',
  accountId: 'account-1',
  card_id: 'MemoryAllocationCard',
  container_name: 'relay-server',
  cloud_resourse: { meta: { namespace: 'nudgebee', controller: 'relay-server', controllerKind: 'Deployment', container: 'relay-server' } },
  // What the evidence path produces when the metric series are missing: zeros.
  memory: { request: undefined, limit: undefined, oldRequest: 0, oldLimit: 0 },
  cpu: { request: undefined, limit: undefined, oldRequest: 0, oldLimit: undefined },
};

const renderForm = (props = {}) =>
  render(
    <KubernetesRightSizingUpdateForm
      open
      title='Tune Resource Configuration - relay-server'
      onClose={jest.fn()}
      onSuccess={jest.fn()}
      onFailure={jest.fn()}
      data={baseData}
      updateResourceType='resourceChange'
      recommendationSource='event'
      {...props}
    />
  );

beforeEach(() => {
  jest.clearAllMocks();
  mockListTicketConfigsForCreate.mockResolvedValue({ data: [{ tool: 'github', name: 'git-infra' }] });
  mockGetK8sWorkload.mockResolvedValue(workloadWithSpec);
});

describe('KubernetesRightSizingUpdateForm — resource tuning', () => {
  it('fills Current from the workload container spec when the event evidence reported none', async () => {
    renderForm();

    // 128Mi request / 307Mi limit, shown in MB for event-sourced tuning.
    await waitFor(() => expect(screen.getByText('128')).toBeInTheDocument());
    expect(screen.getByText('307')).toBeInTheDocument();
    // CPU request 100m -> 0.1 cores; the workload sets no CPU limit.
    expect(screen.getByText('0.1')).toBeInTheDocument();
    expect(screen.getAllByText('Not set').length).toBeGreaterThan(0);
  });

  it('reads a spec written in scientific notation', async () => {
    // Kubernetes accepts "1e-1"/"2e8" as quantities. They used to fail the
    // quantity regex outright and read as unset.
    mockGetK8sWorkload.mockResolvedValue({
      data: {
        k8s_workloads: [
          {
            cloud_resource_id: 'res-1',
            meta: {
              config: {
                annotations: {},
                labels: {},
                containers: [{ name: 'relay-server', resources: { requests: { cpu: '1e-1', memory: '2e8' } } }],
              },
            },
          },
        ],
      },
    });
    renderForm();

    await waitFor(() => expect(screen.getByText('0.1')).toBeInTheDocument());
    // 2e8 bytes -> 190.73 MB
    expect(screen.getByText('190.73')).toBeInTheDocument();
  });

  it('prefills Recommended from the spec base, matching the selected toggles', async () => {
    renderForm();

    // Memory defaults to the +10% buffer toggle: 128 * 1.1 and 307 * 1.1.
    await waitFor(() => {
      const values = Array.from(document.querySelectorAll('input')).map((i) => i.value);
      expect(values).toContain('140.8');
      expect(values).toContain('337.7');
      // CPU defaults to no buffer: the spec request unchanged.
      expect(values).toContain('0.10');
    });
  });

  it('does not gate Raise PR on log analysis for a resource-tuning event', async () => {
    renderForm({ updateResourceType: 'raise-pr' });

    await waitFor(() => expect(mockListTicketConfigsForCreate).toHaveBeenCalled());
    expect(screen.queryByTestId('raise-pr-analysis-gate')).not.toBeInTheDocument();
  });

  it('still gates Raise PR on log analysis for the AI code-fix flow', async () => {
    renderForm({
      updateResourceType: 'raise-pr',
      data: { ...baseData, card_id: 'AskAiCard', aiData: { status: 'IN_PROGRESS', source_details: {} } },
    });

    await waitFor(() => expect(screen.getByTestId('raise-pr-analysis-gate')).toBeInTheDocument());
    expect(screen.getByTestId('raise-pr-analysis-gate')).toHaveTextContent('Log analysis is still running');
  });
});
