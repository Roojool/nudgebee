import React from 'react';
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react';
import '@testing-library/jest-dom';

const mockCanManage = jest.fn();
jest.mock('@lib/auth', () => ({ canManage: (...args: any[]) => mockCanManage(...args) }));

const mockListRules = jest.fn();
const mockGetDefaultPolicies = jest.fn();
const mockUpsertRule = jest.fn();
jest.mock('@api1/notification', () => ({
  __esModule: true,
  default: {
    listRules: (...a: any[]) => mockListRules(...a),
    getDefaultPolicies: (...a: any[]) => mockGetDefaultPolicies(...a),
    upsertRule: (...a: any[]) => mockUpsertRule(...a),
    deleteNotificationRule: jest.fn(),
  },
}));
jest.mock('@api1/home', () => ({
  __esModule: true,
  default: {
    getCloudAccounts: jest.fn(async () => [
      { id: 'a1', account_name: 'k8s-prod', cloud_provider: 'K8s' },
      { id: 'a2', account_name: 'k8s-dev', cloud_provider: 'K8s' },
    ]),
  },
}));
jest.mock('@api1/user', () => ({ __esModule: true, default: { getUserPreferencesTablePageSize: () => 10 } }));
jest.mock('@components/user-management/useAdminAction', () => ({ __esModule: true, default: jest.fn() }));
jest.mock('@components/notifications/rules/destinations', () => ({
  ...jest.requireActual('@components/notifications/rules/destinations'),
  useDestinations: () => [],
}));
jest.mock('@assets', () => ({ DeleteIconRed: 'delete.svg', EditIcon: 'edit.svg' }));
jest.mock('@shared/icons/SafeIcon', () => ({ __esModule: true, default: () => null }));
jest.mock('@shared/icons/IconTextBadge', () => ({
  PlatformChannelBadge: ({ platform, channelName }: any) => <span data-testid={`badge-${platform}`}>{channelName}</span>,
}));
jest.mock('@ui/Label', () => ({ Label: ({ text }: any) => <span data-testid='label'>{text}</span> }));
jest.mock('@ui/Tooltip', () => ({ __esModule: true, default: ({ children }: any) => <>{children}</> }));
jest.mock('@ui/Callout', () => ({
  Callout: ({ message, action }: any) => (
    <div data-testid='callout'>
      {message}
      {action && (
        <button data-testid={action.id} onClick={action.onClick}>
          {action.label}
        </button>
      )}
    </div>
  ),
}));
jest.mock('@ui/Button', () => ({
  Button: ({ children, onClick, id, ['aria-label']: ariaLabel }: any) => (
    <button data-testid={id} aria-label={ariaLabel} onClick={onClick}>
      {children}
    </button>
  ),
}));
jest.mock('@ui/Chip', () => ({
  Chip: ({ children, onClick, id, count, pressed }: any) => (
    <button data-testid={id} aria-pressed={pressed} onClick={onClick}>
      {children} {count}
    </button>
  ),
}));
jest.mock('@ui/Input', () => ({
  Input: ({ id, value, onChange, placeholder }: any) => (
    <input data-testid={id} placeholder={placeholder} value={value} onChange={(e) => onChange(e.target.value)} />
  ),
}));
jest.mock('@ui/DropdownMenu', () => ({
  DropdownMenu: ({ trigger, items }: any) => (
    <div>
      {trigger}
      {items.map((item: any) => (
        <button key={item.id} data-testid={item.id} onClick={item.onSelect}>
          {item.label}
        </button>
      ))}
    </div>
  ),
}));
jest.mock('@ui/ListingLayout', () => {
  const ListingLayout = ({ children }: any) => <div>{children}</div>;
  ListingLayout.Toolbar = ({ children, actions }: any) => (
    <div>
      {children}
      {actions}
    </div>
  );
  ListingLayout.Body = ({ children }: any) => <div>{children}</div>;
  return { ListingLayout };
});
jest.mock('@shared/tables/CustomTable', () => ({
  __esModule: true,
  default: ({ tableData, loading }: any) => (
    <div data-testid='table'>
      {loading && <div data-testid='loading' />}
      {(tableData || []).map((row: any, i: number) => (
        <div key={i} data-testid='row'>
          {row.map((cell: any, j: number) => (
            <span key={j}>{cell.component}</span>
          ))}
        </div>
      ))}
    </div>
  ),
}));
jest.mock('@components/notifications/rules/RuleEditorModal', () => ({
  __esModule: true,
  default: ({ request }: any) =>
    request ? (
      <div data-testid='editor'>
        <span data-testid='editor-new'>{String(request.isNew)}</span>
        <span data-testid='editor-rule'>{JSON.stringify(request.rule)}</span>
        <span data-testid='editor-pause'>{request.pauseFor || ''}</span>
      </div>
    ) : null,
}));
jest.mock('@components/notifications/rules/FallbackModal', () => ({ __esModule: true, default: () => null }));
jest.mock('@components/notifications/rules/TestNotificationModal', () => ({ __esModule: true, default: () => null }));
jest.mock('@components/notifications/rules/OnlySomeAccountsModal', () => ({ __esModule: true, default: () => null }));
jest.mock('@components/notifications/DeleteNotificationRuleModal', () => ({ __esModule: true, default: () => null }));

import Notifications from '@components/notifications';

const row = (patch: Record<string, any>) => ({
  source: 'troubleshoot',
  source_mode: 'list',
  sources: JSON.stringify([patch.source || 'troubleshoot']),
  scope_mode: 'all',
  account_ids: '[]',
  exceptions: '[]',
  is_suppressed: false,
  is_active: true,
  notification_rule_mappings: '[]',
  ...patch,
});

const RULES = [
  row({ id: 'r1', name: 'Silence all troubleshooting', is_suppressed: true, exceptions: '[{"account_ids":["a1"]}]' }),
  row({
    id: 'r2',
    name: 'Dev alerts',
    scope_mode: 'accounts',
    account_ids: '["a2"]',
    notification_rule_mappings: '[{"platform":"slack","channels":{"id":"C2","name":"dev-alerts"}}]',
  }),
  row({ id: 'r3', name: 'Quiet QA', source_mode: 'all', is_suppressed: true, scope_mode: 'accounts', account_ids: '["a2"]' }),
  row({ id: 'r4', name: 'Old optimize', source: 'optimize', sources: '["optimize"]', is_active: false }),
];

const rowsText = () => screen.getAllByTestId('row').map((r) => r.textContent || '');

beforeEach(() => {
  jest.clearAllMocks();
  mockCanManage.mockReturnValue(true);
  mockListRules.mockResolvedValue({ rows: RULES });
  mockGetDefaultPolicies.mockResolvedValue({ policies: [{ source: 'cloud', action: 'drop', mappings: [] }] });
  mockUpsertRule.mockResolvedValue({ id: 'r1' });
});

describe('Notification rules page', () => {
  it('reads each rule as a sentence, with "When no rule matches" last', async () => {
    render(<Notifications />);
    await waitFor(() => expect(screen.getAllByTestId('row')).toHaveLength(5));
    const text = rowsText();
    expect(text[0]).toContain('Troubleshooting alerts from all accounts except k8s-prod are never sent');
    expect(text[1]).toContain('Troubleshooting alerts from k8s-dev go to #dev-alerts');
    expect(text[2]).toContain('All notifications from k8s-dev are never sent');
    expect(text[3]).toContain('Off');
    expect(text[4]).toContain('When no rule matches');
    expect(text[4]).toContain('Notifications no rule covers go to each app’s default channel');
    expect(text[4]).toContain('Cloud spend summaries are not sent instead');
    expect(screen.getByTestId('filter-all')).toHaveTextContent('All 4');
    // The all-types rule counts under every type it covers.
    expect(screen.getByTestId('filter-slo')).toHaveTextContent('SLO 1');
  });

  it('filters by type and shows that type’s fallback', async () => {
    render(<Notifications />);
    await waitFor(() => expect(screen.getAllByTestId('row')).toHaveLength(5));
    fireEvent.click(screen.getByTestId('filter-cloud'));
    const text = rowsText();
    expect(text).toHaveLength(2);
    expect(text[0]).toContain('Quiet QA');
    expect(text[1]).toContain('Cloud spend summaries no rule covers are not sent');
  });

  it('searches rule names and sentences', async () => {
    render(<Notifications />);
    await waitFor(() => expect(screen.getAllByTestId('row')).toHaveLength(5));
    fireEvent.change(screen.getByTestId('search-rules'), { target: { value: 'dev-alerts' } });
    expect(rowsText()).toHaveLength(2);
    fireEvent.change(screen.getByTestId('search-rules'), { target: { value: 'k8s-dev' } });
    expect(rowsText()[0]).toContain('Dev alerts');
  });

  it('flags a Send rule that a Don’t send stops, and lets the account through in one click', async () => {
    render(<Notifications />);
    await waitFor(() => expect(screen.getAllByTestId('row')).toHaveLength(5));
    const devRow = screen.getAllByTestId('row')[1];
    expect(within(devRow).getByTestId('callout')).toHaveTextContent('Never sends: “Silence all troubleshooting” stops everything this rule covers.');
    fireEvent.click(within(devRow).getByTestId('fix-r2'));
    await waitFor(() => expect(mockUpsertRule).toHaveBeenCalled());
    expect(mockUpsertRule.mock.calls[0][0]).toMatchObject({ id: 'r1', exceptions: [{ account_ids: ['a1', 'a2'] }], is_suppressed: true });
  });

  it('opens the editor from the Add rule menu with the chosen starting point', async () => {
    render(<Notifications />);
    await waitFor(() => expect(screen.getAllByTestId('row')).toHaveLength(5));
    fireEvent.click(screen.getByTestId('add-rule-pause'));
    expect(screen.getByTestId('editor-new')).toHaveTextContent('true');
    expect(screen.getByTestId('editor-pause')).toHaveTextContent('24h');
    expect(JSON.parse(screen.getByTestId('editor-rule').textContent || '{}')).toMatchObject({
      types: ['troubleshoot', 'slo'],
      action: 'drop',
      scope: 'some',
    });
  });

  it('turns an off rule on from its menu', async () => {
    render(<Notifications />);
    await waitFor(() => expect(screen.getAllByTestId('row')).toHaveLength(5));
    fireEvent.click(screen.getByTestId('Old optimize-turn-on'));
    await waitFor(() => expect(mockUpsertRule).toHaveBeenCalled());
    expect(mockUpsertRule.mock.calls[0][0]).toMatchObject({ id: 'r4', is_active: true });
  });

  it('shows no edit controls without write access', async () => {
    mockCanManage.mockReturnValue(false);
    render(<Notifications />);
    await waitFor(() => expect(screen.getAllByTestId('row')).toHaveLength(5));
    expect(screen.queryByTestId('notification-rule')).not.toBeInTheDocument();
    expect(screen.queryByTestId('edit-fallback')).not.toBeInTheDocument();
    expect(screen.queryByTestId('fix-r2')).not.toBeInTheDocument();
    expect(screen.getByTestId('test-notification')).toBeInTheDocument();
  });
});
