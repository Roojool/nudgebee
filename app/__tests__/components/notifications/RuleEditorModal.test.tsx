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

import RuleEditorModal, { EditorRequest } from '@components/notifications/rules/RuleEditorModal';
import { newRule, Rule } from '@components/notifications/rules/model';

const ACCOUNTS = [
  { id: 'a1', name: 'k8s-prod', provider: 'K8s' },
  { id: 'a2', name: 'k8s-dev', provider: 'K8s' },
];
const DESTINATIONS = [
  { platform: 'slack', label: 'Slack', options: [{ value: 'C1', label: 'prod-alerts' }], loading: false, installationId: 'inst-1' },
];

const renderEditor = (request: EditorRequest, rules: Rule[] = []) => {
  const onSaved = jest.fn();
  render(<RuleEditorModal request={request} rules={rules} accounts={ACCOUNTS} destinations={DESTINATIONS} onClose={jest.fn()} onSaved={onSaved} />);
  return onSaved;
};

beforeEach(() => {
  jest.clearAllMocks();
  mockUpsertRule.mockResolvedValue({ id: 'new' });
});

describe('RuleEditorModal', () => {
  it('saves a new rule with the suggested name and the picked channel', async () => {
    const onSaved = renderEditor({ rule: newRule(), isNew: true });
    fireEvent.change(screen.getByTestId('rule-scope'), { target: { value: 'some' } });
    const accountSelect = screen.getByTestId('account-1') as HTMLSelectElement;
    (accountSelect.querySelector('option[value="a1"]') as HTMLOptionElement).selected = true;
    fireEvent.change(accountSelect);
    fireEvent.change(screen.getByTestId('slack-channel'), { target: { value: 'C1' } });
    expect(document.getElementById('rule-summary')).toHaveTextContent('Troubleshooting alerts from k8s-prod go to #prod-alerts.');
    fireEvent.click(screen.getByTestId('save-rule'));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(mockUpsertRule.mock.calls[0][0]).toMatchObject({
      name: 'Troubleshooting alerts from k8s-prod to prod-alerts',
      source: 'troubleshoot',
      sources: ['troubleshoot'],
      scope_mode: 'accounts',
      account_ids: ['a1'],
      is_suppressed: false,
      mappings: [{ platform: 'slack', channels: { id: 'C1', name: 'prod-alerts' }, installation_id: 'inst-1' }],
    });
    expect(mockToastSuccess).toHaveBeenCalledWith('Rule Created Successfully');
  });

  it('closes cleanly when the page clears the request after a save', async () => {
    const request: EditorRequest = { rule: newRule({ action: 'drop' }), isNew: true };
    const props = { rules: [], accounts: ACCOUNTS, destinations: DESTINATIONS, onClose: jest.fn() };
    const { rerender } = render(<RuleEditorModal request={request} {...props} onSaved={jest.fn()} />);
    rerender(<RuleEditorModal request={null} {...props} onSaved={jest.fn()} />);
    expect(screen.queryByText('New rule')).not.toBeInTheDocument();
  });

  it('asks for a channel before sending anywhere', () => {
    renderEditor({ rule: newRule(), isNew: true });
    fireEvent.click(screen.getByTestId('save-rule'));
    expect(screen.getByRole('alert')).toHaveTextContent('Pick at least one channel.');
    expect(mockUpsertRule).not.toHaveBeenCalled();
  });

  it('saves a timed Don’t send with its end time', async () => {
    const onSaved = renderEditor({ rule: newRule({ types: ['troubleshoot', 'slo'] }), isNew: true });
    fireEvent.click(screen.getByTestId('action-drop'));
    fireEvent.change(screen.getByTestId('rule-pause-for'), { target: { value: '24h' } });
    expect(screen.getByTestId('status')).toHaveTextContent(/Paused · 2[34]h left/);
    fireEvent.click(screen.getByTestId('save-rule'));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    const input = mockUpsertRule.mock.calls[0][0];
    expect(input).toMatchObject({ is_suppressed: true, name: 'Pause troubleshooting alerts and SLO alerts' });
    const until = new Date(input.expires_at.replace('local:', '')).getTime();
    expect(until - Date.now()).toBeGreaterThan(23.9 * 3600e3);
  });

  it('covers every type with the All types chip', async () => {
    const onSaved = renderEditor({ rule: newRule({ action: 'drop' }), isNew: true });
    fireEvent.click(screen.getByTestId('rule-type-all'));
    fireEvent.click(screen.getByTestId('save-rule'));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(mockUpsertRule.mock.calls[0][0]).toMatchObject({ source_mode: 'all', source: 'troubleshoot', is_suppressed: true });
    expect(mockUpsertRule.mock.calls[0][0].sources).toBeUndefined();
  });

  it('has no account choice for the weekly digest', () => {
    renderEditor({ rule: newRule({ types: ['weekly_digest'] }), isNew: true });
    expect(screen.queryByTestId('rule-scope')).not.toBeInTheDocument();
    expect(screen.getByText('Weekly digests always cover every account.')).toBeInTheDocument();
  });

  it('keeps an older snooze on save, and clears it with Resume now', async () => {
    const until = new Date(Date.now() + 5 * 3600e3).toISOString();
    const rule = newRule({ id: 'r9', name: 'Snoozed', until, mappings: [{ platform: 'slack', channels: { id: 'C1', name: 'prod-alerts' } }] });
    const onSaved = renderEditor({ rule, isNew: false });
    expect(screen.getByTestId('status')).toHaveTextContent('Paused · 5h left');
    fireEvent.click(screen.getByTestId('rule-resume'));
    fireEvent.click(screen.getByTestId('save-rule'));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(mockUpsertRule.mock.calls[0][0]).toMatchObject({ id: 'r9', expires_at: null, is_suppressed: false });
    expect(mockToastSuccess).toHaveBeenCalledWith('Rule Updated Successfully');
  });

  it('warns when another rule stops everything this one sends', () => {
    const silence = newRule({ id: 's', name: 'Silence all', action: 'drop' });
    renderEditor({ rule: newRule({ scope: 'some', accounts: ['a2'] }), isNew: true }, [silence]);
    expect(screen.getByTestId('callout')).toHaveTextContent('Never sends: “Silence all” stops everything this rule covers.');
  });

  it('offers hourly grouping only while nothing is left out', () => {
    renderEditor({ rule: newRule({ except: ['a2'] }), isNew: true });
    expect(screen.getByTestId('rule-batch')).toBeDisabled();
  });

  it('refuses hourly grouping with accounts left out', () => {
    renderEditor({ rule: newRule({ batch: true, except: ['a2'] }), isNew: true });
    fireEvent.change(screen.getByTestId('slack-channel'), { target: { value: 'C1' } });
    fireEvent.click(screen.getByTestId('save-rule'));
    expect(screen.getByRole('alert')).toHaveTextContent('Hourly grouping can’t leave accounts or alerts out.');
    expect(mockUpsertRule).not.toHaveBeenCalled();
  });

  it('rejects a name another rule has', () => {
    renderEditor({ rule: newRule({ action: 'drop' }), isNew: true }, [newRule({ id: 'x', name: 'Quiet', action: 'drop' })]);
    fireEvent.change(screen.getByTestId('notificationName'), { target: { value: 'quiet' } });
    fireEvent.click(screen.getByTestId('save-rule'));
    expect(screen.getByRole('alert')).toHaveTextContent('A notification rule with this name already exists.');
  });
});
