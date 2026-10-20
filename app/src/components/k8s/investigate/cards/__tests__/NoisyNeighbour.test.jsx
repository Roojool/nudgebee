import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import NoisyNeighbour from '../NoisyNeighbour';

// The payload below is the shape noisyNeighboursResponse emits, taken from the
// real dev event on node ...-7iiz where openobserve and vmsingle were OOM-killed
// two seconds apart in different namespaces (#39083).
const neighbour = (podName, namespace, memoryUsed, extra = {}) => ({
  name: 'app',
  pod_name: podName,
  namespace,
  node_name: 'gke-node-7iiz',
  memory_used: memoryUsed,
  memory_requested: 0,
  memory_limit: 0,
  ...extra,
});

const eventWith = (data, aggregationKey = 'pod_oom_killer_enricher') => ({
  aggregation_key: aggregationKey,
  cluster: 'k8s-dev',
  evidences: [
    {
      type: 'json',
      data: JSON.stringify({
        name: 'noisy_neighbours',
        data: {
          node_name: 'gke-node-7iiz',
          memory_allocatable: 12 * 1024 ** 3,
          memory_used: 6 * 1024 ** 3,
          memory_requested: 11.9 * 1024 ** 3,
          pods_on_node: 63,
          subject_pod: 'openobserve-collector-0',
          subject_measured: true,
          neighbours: [],
          ...data,
        },
      }),
    },
  ],
});

describe('NoisyNeighbour card', () => {
  it('pins the subject even when it is nowhere near the top of the ranking', async () => {
    render(
      <NoisyNeighbour
        row={eventWith({
          neighbours: [
            neighbour('hog-1', 'demo', 5 * 1024 ** 3),
            neighbour('hog-2', 'demo', 4 * 1024 ** 3),
            neighbour('hog-3', 'demo', 3 * 1024 ** 3),
            neighbour('hog-4', 'demo', 2 * 1024 ** 3),
            neighbour('openobserve-collector-0', 'openobserve', 1024 ** 3, { is_subject: true }),
          ],
        })}
      />
    );

    // Visible without expanding, despite ranking 5th of 5 — this is the row the
    // reader opened the card for, and it was absent from 494 of 730 real cards.
    const subjectRow = screen.getByTestId('noisy-neighbour-row-openobserve-collector-0');
    expect(within(subjectRow).getByText('this pod')).toBeInTheDocument();
    // The 4th-ranked pod is NOT shown: only the top 3 are, plus the pinned rows.
    expect(screen.queryByText('hog-4')).not.toBeInTheDocument();

    await userEvent.click(screen.getByText('Show all 5 workloads'));
    expect(screen.getByText('hog-4')).toBeInTheDocument();
  });

  // The defect itself: another pod died on the same machine and nothing said so.
  it('shows a co-killed pod that never appeared in the metrics, and says when it died', () => {
    render(
      <NoisyNeighbour
        row={eventWith({
          neighbours: [neighbour('openobserve-collector-0', 'openobserve', 1024 ** 3, { is_subject: true })],
          co_killed: [{ pod_name: 'vmsingle-victoria', namespace: 'victoria', offset_seconds: -2 }],
        })}
      />
    );

    const coKilledRow = screen.getByTestId('noisy-neighbour-row-vmsingle-victoria');
    expect(within(coKilledRow).getByText('OOM-killed 2s before')).toBeInTheDocument();
    // It has no usage reading at all. "not sampled" must not become a 0%, which
    // would read as a pod that was idle when the machine killed it.
    expect(within(coKilledRow).getByText('not sampled')).toBeInTheDocument();
  });

  it('collapses the secondary metric to a single line that still states a verdict', async () => {
    render(
      <NoisyNeighbour
        row={eventWith({
          neighbours: [neighbour('openobserve-collector-0', 'openobserve', 1024 ** 3, { is_subject: true })],
          cpu_allocatable: 4,
          cpu_used: 0.75,
          cpu_neighbours: [{ name: 'app', pod_name: 'hog-1', namespace: 'demo', cpu_used: 0.5, cpu_requested: 0.1, cpu_limit: 1 }],
        })}
      />
    );

    // One line, not a bar and fifteen rows — but it does not make the reader open
    // it to learn there is nothing there.
    expect(screen.getByText(/750 m of 4.00 cores \(19%\) — no pressure/)).toBeInTheDocument();
    expect(screen.queryByText('hog-1')).not.toBeInTheDocument();

    await userEvent.click(screen.getByText(/no pressure/));
    expect(screen.getByText('hog-1')).toBeInTheDocument();
  });

  // A throttled pod is a CPU story; leading with memory there is what made the
  // reader scan two full tables to find the relevant three rows.
  it('leads with CPU for a throttling alert and demotes memory', () => {
    render(
      <NoisyNeighbour
        row={eventWith(
          {
            neighbours: [neighbour('mem-hog', 'demo', 2 * 1024 ** 3)],
            cpu_allocatable: 4,
            cpu_used: 3.9,
            cpu_neighbours: [{ name: 'app', pod_name: 'cpu-hog', namespace: 'demo', cpu_used: 3, cpu_requested: 0.1, cpu_limit: 1 }],
          },
          'CPUThrottlingHigh'
        )}
      />
    );

    // CPU leads: its consumers are on screen and memory's are not.
    expect(screen.getByText('cpu-hog')).toBeInTheDocument();
    expect(screen.queryByText('mem-hog')).not.toBeInTheDocument();
    // Memory is demoted to the one-line secondary, verdict included. The line is
    // assembled from several text nodes, so match on the element's own text.
    expect(screen.getByText((_, el) => el?.textContent === '6 GiB of 12 GiB (50%) — no pressure')).toBeInTheDocument();
  });

  it('renders nothing when the event carries no noisy-neighbours card', () => {
    const { container } = render(<NoisyNeighbour row={{ evidences: [] }} />);
    expect(container).toBeEmptyDOMElement();
  });
});
