import React from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom';
import apiKubernetes from '@api1/kubernetes';
import { EventStateRows, LinkedEventState } from '@components/k8s/investigate/EventStateRows';

jest.mock('next/router', () => ({
  useRouter: () => ({ push: jest.fn(), replace: jest.fn(), query: {}, pathname: '/investigate', asPath: '/investigate' }),
}));

jest.mock('@api1/kubernetes', () => ({
  __esModule: true,
  default: {
    getK8sEventGroupings: jest.fn(),
    getEventState: jest.fn(),
  },
}));

const row = {
  id: 'e1',
  cloud_account_id: 'acc-1',
  fingerprint: 'fp-1',
  status: 'CLOSED',
  starts_at: '2026-09-14 09:00:00',
  ends_at: '2026-09-14 09:05:00',
  created_at: '2026-09-14 09:00:02.197584',
};

describe('EventStateRows', () => {
  beforeEach(() => jest.clearAllMocks());

  // created_at carries microseconds and the filter only milliseconds, so the lookup must start past
  // the event's own created_at or it counts itself as a recurrence.
  it('looks up newer events with the same fingerprint, starting after the event itself', async () => {
    apiKubernetes.getK8sEventGroupings.mockResolvedValue({
      data: {
        event_groupings: [{ id: 'e9', status: 'FIRING', starts_at: '2026-09-14 10:00:00' }],
        event_groupings_aggregate: { aggregate: { count: 4 } },
      },
    });
    render(<EventStateRows row={row} />);

    expect(screen.getByText('Ended')).toBeInTheDocument();
    expect(screen.getByText('lasted 5m')).toBeInTheDocument();

    const [limit, , query] = apiKubernetes.getK8sEventGroupings.mock.calls[0];
    expect(limit).toBe(1);
    expect(query.account_id).toBe('acc-1');
    expect(query.fingerprint).toBe('fp-1');
    expect(query.start_date.toISOString()).toBe('2026-09-14T09:00:03.197Z');

    expect(await screen.findByText('Came back')).toBeInTheDocument();
    expect(screen.getByTestId('event-recurred-since')).toHaveTextContent('4× since');
    expect(screen.getByTestId('event-recurred-since').querySelector('a')).toHaveAttribute('href', '/investigate?id=e9&accountId=acc-1');
  });

  it('shows no recurrence row when nothing newer exists', async () => {
    apiKubernetes.getK8sEventGroupings.mockResolvedValue({ data: { event_groupings: [], event_groupings_aggregate: { aggregate: { count: 0 } } } });
    render(<EventStateRows row={row} />);
    await waitFor(() => expect(apiKubernetes.getK8sEventGroupings).toHaveBeenCalled());
    expect(screen.queryByTestId('event-recurred-since')).not.toBeInTheDocument();
  });

  // Demo groupings are canned mock data; counting them would invent recurrences.
  it('does not look up recurrences on the demo account', () => {
    render(<EventStateRows row={{ ...row, cloud_account_id: 'demo' }} />);
    expect(apiKubernetes.getK8sEventGroupings).not.toHaveBeenCalled();
  });
});

describe('LinkedEventState', () => {
  beforeEach(() => jest.clearAllMocks());

  it("shows the linked event's state", async () => {
    apiKubernetes.getEventState.mockResolvedValue({ id: 'o1', status: 'FIRING', starts_at: '2026-09-14 09:00:00', ends_at: null });
    render(<LinkedEventState eventId='o1' accountId='acc-1' />);
    expect(await screen.findByText('Open')).toBeInTheDocument();
    expect(apiKubernetes.getEventState).toHaveBeenCalledWith('o1');
  });

  it('does not look up on the demo account', () => {
    render(<LinkedEventState eventId='o1' accountId='demo' />);
    expect(apiKubernetes.getEventState).not.toHaveBeenCalled();
  });
});
