import React from 'react';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import '@testing-library/jest-dom';
import ApiTokens from '@shared/settings/ApiTokens';

jest.mock('@utils/colors');

jest.mock('@ui/Modal', () => ({
  __esModule: true,
  Modal: ({ open, onClose, onConfirm, confirmText, title, children, actionButtons }) =>
    open ? (
      <div data-testid='modal'>
        <h2>{title}</h2>
        {children}
        {actionButtons}
        {!actionButtons && confirmText && (
          <>
            <button onClick={onClose} data-testid='btn-Cancel'>
              Cancel
            </button>
            <button onClick={onConfirm} data-testid={`btn-${confirmText}`}>
              {confirmText}
            </button>
          </>
        )}
      </div>
    ) : null,
}));

jest.mock('@ui/Button', () => ({
  __esModule: true,
  Button: ({ children, onClick, disabled, loading }) => (
    <button onClick={onClick} disabled={disabled || loading} data-testid={`btn-${children}`}>
      {children}
    </button>
  ),
}));

jest.mock('@ui/Input', () => ({
  __esModule: true,
  Input: ({ label, value, onChange, placeholder }) => (
    <input
      aria-label={label}
      value={value || ''}
      placeholder={placeholder}
      onChange={(e) => onChange(e.target.value)}
      data-testid={`field-${label}`}
    />
  ),
}));

jest.mock('@ui/Select', () => ({
  __esModule: true,
  Select: ({ label, value, onChange, options }) => (
    <select aria-label={label} value={value ?? ''} onChange={(e) => onChange(e.target.value)} data-testid={`select-${label}`}>
      {options.map((o) => {
        const opt = typeof o === 'string' ? { value: o, label: o } : o;
        return (
          <option key={opt.value} value={opt.value}>
            {opt.label}
          </option>
        );
      })}
    </select>
  ),
}));

jest.mock('@ui/Banner', () => ({
  __esModule: true,
  Banner: ({ tone, title, message }) => (
    <div data-testid={`banner-${tone}`}>
      {title}
      {message}
    </div>
  ),
}));

jest.mock('@ui/EmptyState', () => ({
  __esModule: true,
  EmptyState: ({ title }) => <div data-testid='empty-state'>{title}</div>,
}));

jest.mock('@shared/navigation/Tabs', () => ({
  __esModule: true,
  default: ({ options, value, onChange }) => (
    <div role='tablist'>
      {(options?.tabOptions || [])
        .filter((o) => !o.hidden)
        .map((o) => (
          <button key={o.value} role='tab' aria-selected={o.value === value} onClick={() => onChange(o.value)}>
            {o.text}
          </button>
        ))}
    </div>
  ),
}));

jest.mock('@ui/Toast', () => ({
  toast: { success: jest.fn(), error: jest.fn() },
  snackbar: { success: jest.fn(), error: jest.fn() },
}));

jest.mock('@shared/tables/CustomTable', () => ({
  __esModule: true,
  default: ({ tableData }) => (
    <table>
      <tbody>
        {tableData.map((row, i) => (
          <tr key={i}>
            {row.map((cell, j) => (
              <td key={j}>{cell.component || cell.text}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  ),
}));

jest.mock('@shared/icons/SafeIcon', () => ({
  __esModule: true,
  default: ({ alt, ...props }) => React.createElement('img', { alt, 'data-testid': 'safe-icon', ...props }),
}));

jest.mock('@assets', () => ({
  DeleteIconRed: 'delete-icon-red.svg',
}));

jest.mock('@lib/externalUrls', () => ({
  getAppBaseUrl: () => 'http://localhost:3000',
}));

const mockListUserTokens = jest.fn().mockResolvedValue({ data: [] });
const mockCreateUserToken = jest.fn().mockResolvedValue({ data: { token: 'new-token-value-xyz' } });
const mockDeleteUserToken = jest.fn().mockResolvedValue({ data: {} });

jest.mock('@api1/user/', () => ({
  __esModule: true,
  default: {
    listUserTokens: (...args) => mockListUserTokens(...args),
    createUserToken: (...args) => mockCreateUserToken(...args),
    deleteUserToken: (...args) => mockDeleteUserToken(...args),
  },
}));

const mockGetTokenScopeCatalog = jest.fn();
jest.mock('@api1/roles', () => ({
  getTokenScopeCatalog: (...args) => mockGetTokenScopeCatalog(...args),
}));

const SCOPE_CATALOG = [
  { module: 'tickets', scope: 'account', classes: ['Read', 'Write'], actions: {} },
  { module: 'workflows', scope: 'account', classes: ['Read', 'Write', 'Execute'], actions: {} },
  { module: 'users', scope: 'tenant', classes: ['Read', 'Write'], actions: {} },
];

jest.mock('src/utils/common', () => ({
  parseHttpResponseBodyMessage: jest.fn().mockReturnValue('Error occurred'),
}));

describe('ApiTokens', () => {
  const defaultProps = {
    open: true,
    title: 'API Tokens',
    onClose: jest.fn(),
  };

  beforeEach(() => {
    jest.clearAllMocks();
    mockListUserTokens.mockResolvedValue({ data: [] });
    mockCreateUserToken.mockResolvedValue({ data: { token: 'new-token-value-xyz' } });
    mockDeleteUserToken.mockResolvedValue({ data: {} });
    mockGetTokenScopeCatalog.mockResolvedValue(SCOPE_CATALOG);
  });

  it('renders modal when open is true', async () => {
    render(<ApiTokens {...defaultProps} />);
    await waitFor(() => {
      expect(screen.getByTestId('modal')).toBeInTheDocument();
    });
    expect(screen.getByRole('heading', { name: 'API Tokens' })).toBeInTheDocument();
  });

  it('does not render modal when open is false', () => {
    render(<ApiTokens {...defaultProps} open={false} />);
    expect(screen.queryByTestId('modal')).not.toBeInTheDocument();
  });

  it('shows empty state when no tokens', async () => {
    render(<ApiTokens {...defaultProps} />);
    await waitFor(() => {
      expect(screen.getByTestId('empty-state')).toBeInTheDocument();
    });
    expect(screen.getByText('No platform keys yet')).toBeInTheDocument();
  });

  it('shows Create Token button initially', async () => {
    render(<ApiTokens {...defaultProps} />);
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /create token/i })).toBeInTheDocument();
    });
  });

  it('splits keys into platform, gateway and all-access tabs', async () => {
    mockListUserTokens.mockResolvedValue({
      data: [
        {
          id: '1',
          name: 'nbctl',
          kind: 'platform',
          status: 'active',
          created_at: '2026-09-01T00:00:00Z',
          accessed_at: null,
          expires_at: null,
          limits: [],
        },
        {
          id: '2',
          name: 'claude-code',
          kind: 'gateway',
          status: 'active',
          created_at: '2026-09-02T00:00:00Z',
          accessed_at: null,
          expires_at: '2026-12-21T00:00:00Z',
          limits: [{ metric: 'cost', period: 'month', value: 20, model: '' }],
        },
        {
          id: '3',
          name: 'old',
          kind: 'platform',
          status: 'active',
          created_at: '2025-01-01T00:00:00Z',
          accessed_at: null,
          expires_at: null,
          limits: [],
        },
      ],
    });
    render(<ApiTokens open title='API Tokens' onClose={() => {}} />);
    expect(await screen.findByRole('tab', { name: /platform keys/i })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /ai gateway keys/i })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: /all-access keys/i })).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: /legacy/i })).not.toBeInTheDocument();
  });

  it('an all-access key create sends kind=all and carries limits', async () => {
    mockCreateUserToken.mockResolvedValue({ data: { token: 'sk-nb-xyz', name: 'shared', kind: 'all' } });
    render(<ApiTokens open title='API Tokens' onClose={() => {}} />);
    // The active tab is the kind Create mints, so 'all' is only reachable deliberately.
    await userEvent.click(await screen.findByRole('tab', { name: /all-access keys/i }));
    await userEvent.click(screen.getByRole('button', { name: /create token/i }));
    await userEvent.type(screen.getByLabelText(/name/i), 'shared');
    await userEvent.click(screen.getByRole('button', { name: /add limit/i }));
    await userEvent.type(screen.getByLabelText(/value/i), '20');
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }));
    await waitFor(() => {
      expect(mockCreateUserToken).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'shared',
          kind: 'all',
          limits: [expect.objectContaining({ metric: 'cost', period: 'month', value: 20 })],
        })
      );
    });
  });

  it('a platform key create sends no limits', async () => {
    mockListUserTokens.mockResolvedValue({ data: [] });
    mockCreateUserToken.mockResolvedValue({ data: { token: 'sk-nb-xyz', name: 'nbctl', kind: 'platform' } });
    render(<ApiTokens open title='API Tokens' onClose={() => {}} />);
    await userEvent.click(await screen.findByRole('button', { name: /create token/i }));
    await userEvent.type(screen.getByLabelText(/name/i), 'nbctl');
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }));
    expect(mockCreateUserToken).toHaveBeenCalledWith(expect.objectContaining({ name: 'nbctl', kind: 'platform', limits: [] }));
  });

  it('a full-access platform key sends no scopes', async () => {
    mockCreateUserToken.mockResolvedValue({ data: { token: 'sk-nb-xyz', name: 'ci', kind: 'platform' } });
    render(<ApiTokens open title='API Tokens' onClose={() => {}} />);
    await userEvent.click(await screen.findByRole('button', { name: /create token/i }));
    await userEvent.type(screen.getByLabelText(/name/i), 'ci');
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }));
    expect(mockCreateUserToken).toHaveBeenCalledWith(expect.objectContaining({ kind: 'platform', scopes: null }));
    expect(mockGetTokenScopeCatalog).not.toHaveBeenCalled();
  });

  it('a restricted platform key sends the ticked scopes; unticking Read clears Write', async () => {
    mockCreateUserToken.mockResolvedValue({ data: { token: 'sk-nb-xyz', name: 'ci', kind: 'platform' } });
    render(<ApiTokens open title='API Tokens' onClose={() => {}} />);
    await userEvent.click(await screen.findByRole('button', { name: /create token/i }));
    await userEvent.type(screen.getByLabelText(/name/i), 'ci');
    await userEvent.click(screen.getByRole('radio', { name: /restricted/i }));
    expect(await screen.findByText(/started by this key use your full access/i)).toBeInTheDocument();
    // Write stays disabled until Read is ticked, as in the role editor.
    expect(await screen.findByRole('checkbox', { name: /tickets — write/i })).toBeDisabled();
    await userEvent.click(screen.getByRole('checkbox', { name: /tickets — read/i }));
    await userEvent.click(screen.getByRole('checkbox', { name: /tickets — write/i }));
    await userEvent.click(screen.getByRole('checkbox', { name: /automations — read/i }));
    await userEvent.click(screen.getByRole('checkbox', { name: /automations — write/i }));
    await userEvent.click(screen.getByRole('checkbox', { name: /automations — read/i }));
    await userEvent.click(screen.getByRole('checkbox', { name: /automations — execute/i }));
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }));
    expect(mockCreateUserToken).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'platform', scopes: ['tickets:Read', 'tickets:Write', 'workflows:Execute'] })
    );
  });

  it('keeps Create disabled for a restricted key with no scope ticked', async () => {
    render(<ApiTokens open title='API Tokens' onClose={() => {}} />);
    await userEvent.click(await screen.findByRole('button', { name: /create token/i }));
    await userEvent.type(screen.getByLabelText(/name/i), 'ci');
    await userEvent.click(screen.getByRole('radio', { name: /restricted/i }));
    await screen.findByRole('checkbox', { name: /tickets — read/i });
    expect(screen.getByRole('button', { name: /^create$/i })).toBeDisabled();
  });

  it('shows each platform key as full access or its scope count', async () => {
    const base = { status: 'active', created_at: '2026-09-01T00:00:00Z', accessed_at: null, expires_at: null, limits: [], kind: 'platform' };
    mockListUserTokens.mockResolvedValue({
      data: [
        { ...base, id: '1', name: 'open', scopes: null },
        { ...base, id: '2', name: 'narrow', scopes: ['tickets:Read', 'tickets:Write'] },
      ],
    });
    render(<ApiTokens open title='API Tokens' onClose={() => {}} />);
    expect(await screen.findByText('Full access')).toBeInTheDocument();
    expect(screen.getByText('2 scopes')).toBeInTheDocument();
  });

  it('a custom expiry in hours sends an expires_at roughly that far ahead', async () => {
    mockListUserTokens.mockResolvedValue({ data: [] });
    mockCreateUserToken.mockResolvedValue({ data: { token: 'sk-nb-xyz', name: 'nbctl', kind: 'platform' } });
    render(<ApiTokens open title='API Tokens' onClose={() => {}} />);
    await userEvent.click(await screen.findByRole('button', { name: /create token/i }));
    await userEvent.type(screen.getByLabelText(/name/i), 'nbctl');
    await userEvent.selectOptions(screen.getByLabelText(/^expires$/i), 'custom');
    await userEvent.selectOptions(screen.getByLabelText(/^unit$/i), 'hours');
    await userEvent.type(screen.getByLabelText(/^duration$/i), '1');
    const before = Date.now();
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }));
    const after = Date.now();
    expect(mockCreateUserToken).toHaveBeenCalledTimes(1);
    const [payload] = mockCreateUserToken.mock.calls[0];
    const expiresAtMs = new Date(payload.expiresAt).getTime();
    const oneHourMs = 60 * 60 * 1000;
    // Range, not an exact string — this test must not flake on clock drift between
    // `before`/`after` and the component's own `Date.now()` call.
    expect(expiresAtMs).toBeGreaterThanOrEqual(before + oneHourMs - 5000);
    expect(expiresAtMs).toBeLessThanOrEqual(after + oneHourMs + 5000);
  });

  it('leaves Create disabled when the custom expiry is empty or zero', async () => {
    mockListUserTokens.mockResolvedValue({ data: [] });
    render(<ApiTokens open title='API Tokens' onClose={() => {}} />);
    await userEvent.click(await screen.findByRole('button', { name: /create token/i }));
    await userEvent.type(screen.getByLabelText(/name/i), 'nbctl');
    await userEvent.selectOptions(screen.getByLabelText(/^expires$/i), 'custom');
    // Empty custom value.
    expect(screen.getByRole('button', { name: /^create$/i })).toBeDisabled();
    // Zero is just as invalid as empty.
    await userEvent.type(screen.getByLabelText(/^duration$/i), '0');
    expect(screen.getByRole('button', { name: /^create$/i })).toBeDisabled();
  });

  it('a gateway key create filters out zero-value limit rows', async () => {
    mockListUserTokens.mockResolvedValue({ data: [] });
    mockCreateUserToken.mockResolvedValue({ data: { token: 'sk-nb-gw-xyz', name: 'claude-code', kind: 'gateway' } });
    render(<ApiTokens open title='API Tokens' onClose={() => {}} />);
    // Switch to the AI Gateway tab before creating so the dialog opens for kind='gateway'.
    await userEvent.click(await screen.findByRole('tab', { name: /ai gateway keys/i }));
    await userEvent.click(screen.getByRole('button', { name: /create token/i }));
    await userEvent.type(screen.getByLabelText(/name/i), 'claude-code');
    await userEvent.click(screen.getByRole('button', { name: /add limit/i }));
    // Leave the value at '' (invalid, filtered out) and add a second, valid limit.
    await userEvent.click(screen.getByRole('button', { name: /add limit/i }));
    const valueInputs = screen.getAllByLabelText(/value/i);
    await userEvent.type(valueInputs[1], '20');
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }));
    expect(mockCreateUserToken).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'claude-code',
        kind: 'gateway',
        limits: [expect.objectContaining({ value: 20 })],
      })
    );
  });

  it('each tab lists only its own kind', async () => {
    mockListUserTokens.mockResolvedValue({
      data: [
        {
          id: '1',
          name: 'nbctl',
          kind: 'platform',
          status: 'active',
          created_at: '2026-09-01T00:00:00Z',
          accessed_at: null,
          expires_at: null,
          limits: [],
        },
        {
          id: '2',
          name: 'shared',
          kind: 'all',
          status: 'active',
          created_at: '2026-09-02T00:00:00Z',
          accessed_at: null,
          expires_at: null,
          limits: [],
        },
      ],
    });
    render(<ApiTokens {...defaultProps} />);
    await waitFor(() => {
      expect(screen.getByText('nbctl')).toBeInTheDocument();
    });
    expect(screen.queryByText('shared')).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole('tab', { name: /all-access keys/i }));
    expect(await screen.findByText('shared')).toBeInTheDocument();
    expect(screen.queryByText('nbctl')).not.toBeInTheDocument();
  });

  it('revokes a key by id', async () => {
    mockListUserTokens.mockResolvedValue({
      data: [
        { id: 'tok-1', name: 'My Token', kind: 'platform', created_at: '2024-01-01T00:00:00Z', accessed_at: null, expires_at: null, limits: [] },
      ],
    });
    render(<ApiTokens {...defaultProps} />);
    await waitFor(() => {
      expect(screen.getByText('My Token')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: /revoke/i }));
    fireEvent.click(screen.getByTestId('btn-Delete'));
    await waitFor(() => {
      expect(mockDeleteUserToken).toHaveBeenCalledWith({ id: 'tok-1' });
    });
  });

  it('calls onClose when Close button is clicked', async () => {
    render(<ApiTokens {...defaultProps} />);
    await waitFor(() => {
      expect(screen.getByTestId('btn-Close')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByTestId('btn-Close'));
    expect(defaultProps.onClose).toHaveBeenCalled();
  });

  it('shows How to use instructions modal when button clicked', async () => {
    render(<ApiTokens {...defaultProps} />);
    await waitFor(() => {
      expect(screen.getByTestId('btn-How to use')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByTestId('btn-How to use'));
    await waitFor(() => {
      expect(screen.getByText('How to use API Tokens')).toBeInTheDocument();
    });
  });
});
