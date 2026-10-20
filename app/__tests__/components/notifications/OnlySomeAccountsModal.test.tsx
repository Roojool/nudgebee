import React from 'react';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom';

const mockUpsertRule = jest.fn();
const mockToastSuccess = jest.fn();
jest.mock('@api1/notification', () => ({ __esModule: true, default: { upsertRule: (...a: any[]) => mockUpsertRule(...a) } }));
jest.mock('@api1/kubernetes', () => ({
  __esModule: true,
  default: { getAllK8sNamespaces: jest.fn(async () => ({ data: [] })), getAllK8sWorkload: jest.fn(async () => ({ data: [] })) },
}));
jest.mock('@api1/kubernetes1', () => ({ __esModule: true, default: { getEventRules: jest.fn(async () => ({ data: { event_rules: [] } })) } }));
jest.mock('@api1/user', () => ({ __esModule: true, default: { listUsers: jest.fn(async () => ({ data: [] })) } }));
jest.mock('@lib/formatter', () => ({ __esModule: true, default: { formatDateWithTimezone: (d: Date) => `local:${d.toISOString()}` } }));
jest.mock('@assets', () => ({}));
jest.mock('@shared/icons/SafeIcon', () => ({ __esModule: true, default: () => null }));
jest.mock('@shared/icons/IconTextBadge', () => ({ PlatformChannelBadge: ({ channelName }: any) => <span>{channelName}</span> }));
jest.mock('@shared/widgets/CustomDateTimePicker', () => ({ __esModule: true, default: () => <input data-testid='date' /> }));
jest.mock('@ui/Toast', () => ({ toast: { success: (...a: any[]) => mockToastSuccess(...a), error: jest.fn() } }));
jest.mock('@ui/Label', () => ({ Label: ({ text }: any) => <span data-testid='status'>{text}</span> }));
jest.mock('@ui/Tooltip', () => ({ __esModule: true, default: ({ children }: any) => <>{children}</> }));
jest.mock('@ui/Link', () => ({ Link: ({ children }: any) => <span>{children}</span> }));
jest.mock('@ui/Modal', () => ({
  Modal: ({ open, children, actionButtons, title }: any) =>
    open ? (
      <div>
        <h2>{title}</h2>
        {children}
        {actionButtons}
      </div>
    ) : null,
}));
jest.mock('@ui/Button', () => ({
  Button: ({ children, onClick, id }: any) => (
    <button data-testid={id} onClick={onClick}>
      {children}
    </button>
  ),
}));
jest.mock('@ui/Chip', () => ({
  Chip: ({ children, onClick, id, pressed, disabled }: any) => (
    <button data-testid={id} aria-pressed={pressed} disabled={disabled} onClick={onClick}>
      {children}
    </button>
  ),
}));
jest.mock('@ui/Input', () => ({
  Input: ({ id, value, onChange, placeholder }: any) => (
    <input data-testid={id} placeholder={placeholder} value={value} onChange={(e) => onChange(e.target.value)} />
  ),
}));
jest.mock('@ui/Select', () => ({
  Select: ({ id, value, onChange, options, multiple }: any) => (
    <select
      data-testid={id}
      multiple={multiple}
      value={multiple ? value : value ?? ''}
      onChange={(e) => onChange(multiple ? Array.from(e.target.selectedOptions).map((o: any) => o.value) : e.target.value)}
    >
      {!multiple && <option value=''>none</option>}
      {options.map((o: any) => (
        <option key={o.value} value={o.value}>
          {typeof o.label === 'string' ? o.label : o.value}
        </option>
      ))}
    </select>
  ),
}));
jest.mock('@ui/ToggleGroup', () => ({
  ToggleGroup: ({ options, onChange, value }: any) => (
    <div>
      {options.map((o: any) => (
        <button key={o.value} data-testid={`action-${o.value}`} aria-pressed={value === o.value} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  ),
}));
jest.mock('@ui/Checkbox', () => ({
  Checkbox: ({ id, checked, onChange, label, disabled }: any) => (
    <label>
      <input type='checkbox' data-testid={id} checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      {label}
    </label>
  ),
}));
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

import OnlySomeAccountsModal from '@components/notifications/rules/OnlySomeAccountsModal';

const ACCOUNTS = [
  { id: 'a1', name: 'k8s-prod', provider: 'K8s' },
  { id: 'a2', name: 'k8s-dev', provider: 'K8s' },
];
const DESTINATIONS = [{ platform: 'slack', label: 'Slack', options: [{ value: 'C1', label: 'prod-alerts' }], loading: false }];

const fill = () => {
  const keep = screen.getByTestId('only-keep-accounts') as HTMLSelectElement;
  (keep.querySelector('option[value="a1"]') as HTMLOptionElement).selected = true;
  fireEvent.change(keep);
  fireEvent.change(screen.getByTestId('only-slack'), { target: { value: 'C1' } });
};

beforeEach(() => jest.clearAllMocks());

describe('OnlySomeAccountsModal', () => {
  it('creates the Send rule before the Don’t send, so a failure never silences the kept accounts', async () => {
    mockUpsertRule.mockResolvedValue({ id: 'x' });
    const onClose = jest.fn();
    render(<OnlySomeAccountsModal open accounts={ACCOUNTS} destinations={DESTINATIONS} rules={[]} onClose={onClose} onSaved={jest.fn()} />);
    fill();
    fireEvent.click(screen.getByTestId('save-only-accounts'));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(mockUpsertRule.mock.calls.map((c) => c[0].is_suppressed)).toEqual([false, true]);
    expect(mockUpsertRule.mock.calls[0][0]).toMatchObject({ scope_mode: 'accounts', account_ids: ['a1'] });
    expect(mockUpsertRule.mock.calls[1][0]).toMatchObject({ scope_mode: 'all', exceptions: [{ account_ids: ['a1'] }] });
  });

  it('retries only the rule that failed', async () => {
    mockUpsertRule.mockResolvedValueOnce({ id: 'send' }).mockResolvedValueOnce({ error: 'Try again' }).mockResolvedValue({ id: 'silence' });
    render(<OnlySomeAccountsModal open accounts={ACCOUNTS} destinations={DESTINATIONS} rules={[]} onClose={jest.fn()} onSaved={jest.fn()} />);
    fill();
    fireEvent.click(screen.getByTestId('save-only-accounts'));
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Try again'));
    fireEvent.click(screen.getByTestId('save-only-accounts'));
    await waitFor(() => expect(mockUpsertRule).toHaveBeenCalledTimes(3));
    expect(mockUpsertRule.mock.calls[2][0].is_suppressed).toBe(true);
  });
});
