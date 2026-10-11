import React from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import KubernetesEvents from '../KubernetesEvents';

/*
 * The date picker on a table embedded in an expanded row (enableFilters=false)
 * scopes that row's list only. On Troubleshoot, start_time/end_time in the URL
 * are the page's scope and key the sub-tab remount, so writing them from a row
 * collapses the row and re-queries the whole inbox.
 */

const applyFiltersOnRouter = jest.fn();
const getK8sEvents = jest.fn();

jest.mock('next/router', () => ({
  useRouter: () => ({
    events: { on: jest.fn(), off: jest.fn() },
    push: jest.fn(),
    replace: jest.fn(),
    asPath: '/troubleshoot',
    pathname: '/troubleshoot',
    query: { start_time: '1000', end_time: '2000' },
  }),
}));

jest.mock('next/dynamic', () => ({ __esModule: true, default: () => () => null }));

jest.mock('@lib/router', () => ({ applyFiltersOnRouter: (...args) => applyFiltersOnRouter(...args) }));

jest.mock('@api1/kubernetes', () => ({
  __esModule: true,
  default: {
    getK8sEvents: (...args) => getK8sEvents(...args),
    getK8sEventsCount: jest.fn(async () => ({ data: { data: { count: 0 } } })),
    getK8sEventGroupings: jest.fn(async () => ({ data: { data: [] } })),
  },
}));
jest.mock('@api1/tickets', () => ({ __esModule: true, default: { listTicketsSummary: jest.fn(async () => ({ data: { data: [] } })) } }));
jest.mock('@api1/user', () => ({ __esModule: true, default: { getUserPreferencesTablePageSize: () => 10 } }));

jest.mock('@lib/auth', () => ({
  hasWriteAccess: () => true,
  hasPermission: () => true,
  getCurrentTenant: () => ({ id: 'tenant-1' }),
  getUserSession: () => ({}),
}));

jest.mock('@hooks/useKubernetesEventFilters', () => ({
  __esModule: true,
  default: () => ({
    accounts: [],
    accountType: 'K8s',
    namespaceFilter: [],
    workloadFilter: [],
    subjectTypeFilter: [],
    aggregationKeyFilter: [],
    sourceFilter: [],
    isOptionsLoading: {},
  }),
}));

jest.mock('@hooks/useCloudFilters', () => ({
  useEventCloudFilter: () => ({ serviceNamesFilter: [], eventNamesFilter: [], isOptionsLoading: false }),
}));

jest.mock('@shared/widgets/CustomDateTimeRangePicker', () => ({
  __esModule: true,
  default: ({ onChange }) => (
    <button data-testid='range-picker' onClick={() => onChange({ selection: { startTime: 5000, endTime: 6000, shortcutClickTime: 0 } })}>
      pick
    </button>
  ),
}));

afterEach(() => {
  cleanup();
  jest.clearAllMocks();
});

const eventsQueryOf = (call) => call[2];

describe('KubernetesEvents date picker', () => {
  beforeEach(() => {
    getK8sEvents.mockResolvedValue({ data: { data: [] } });
  });

  it('keeps an embedded row table range out of the page URL but re-queries with it', async () => {
    await act(async () => {
      render(<KubernetesEvents accountId='acc-1' enableFilters={false} defaultQuery={{ fingerprint: ['fp-1'] }} />);
    });
    await waitFor(() => expect(getK8sEvents).toHaveBeenCalled());
    getK8sEvents.mockClear();

    await act(async () => {
      fireEvent.click(screen.getByTestId('range-picker'));
    });

    await waitFor(() => expect(getK8sEvents).toHaveBeenCalled());
    const query = eventsQueryOf(getK8sEvents.mock.calls.at(-1));
    expect(new Date(query.startDate).getTime()).toBe(5000);
    expect(new Date(query.endDate).getTime()).toBe(6000);
    expect(applyFiltersOnRouter).not.toHaveBeenCalled();
  });

  it('still writes the range to the URL for a top-level table', async () => {
    await act(async () => {
      render(<KubernetesEvents accountId='acc-1' />);
    });
    await waitFor(() => expect(getK8sEvents).toHaveBeenCalled());

    await act(async () => {
      fireEvent.click(screen.getByTestId('range-picker'));
    });

    expect(applyFiltersOnRouter).toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ start_time: 5000, end_time: 6000 }));
  });
});
