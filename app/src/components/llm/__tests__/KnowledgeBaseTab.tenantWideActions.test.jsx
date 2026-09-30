import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import KnowledgeBaseTab from '@components/llm/KnowledgeBaseTab';
import apiKnowledgeBase from '@api1/knowledge-base';
import apiAskNudgebee from '@api1/ask-nudgebee';
import { hasWriteAccess } from '@lib/auth';

jest.mock('@api1/knowledge-base', () => ({
  __esModule: true,
  KB_AGENT_WILDCARD: '*',
  default: {
    getKnowledgeBases: jest.fn(),
    getKnowledgeBase: jest.fn(),
    getKBAgents: jest.fn(),
    updateKnowledgeBase: jest.fn(),
    createKnowledgeBase: jest.fn(),
    deleteKnowledgeBase: jest.fn(),
    retriggerKB: jest.fn(),
    setKnowledgeBaseEnabled: jest.fn(),
    mapKnowledgeBaseToAgent: jest.fn(),
    unmapKnowledgeBaseFromAgent: jest.fn(),
    getKBLoadHistory: jest.fn(),
    testRetrieval: jest.fn(),
  },
}));
jest.mock('@api1/ask-nudgebee', () => ({ __esModule: true, default: { listAgents: jest.fn(), listTools: jest.fn() } }));
jest.mock('@api1/kubernetes', () => ({ __esModule: true, default: { getK8sWorkloadNames: jest.fn(), getK8sNamespaceNames: jest.fn() } }));
jest.mock('@lib/auth', () => ({ hasWriteAccess: jest.fn() }));
jest.mock('@components/llm/ScopeChip', () => ({ __esModule: true, default: () => null, HybridScopeChips: () => null }));
jest.mock('@components/llm/KnowledgePolicySettings', () => ({ __esModule: true, default: () => null }));
jest.mock('@components/llm/KBDocumentsModal', () => ({ __esModule: true, default: () => null }));
jest.mock('@components/llm/KnowledgeBaseFormModal', () => ({
  __esModule: true,
  default: ({ open, onSubmit }) =>
    open ? (
      <button type='button' onClick={() => onSubmit({ name: 'Runbooks', agentIds: [] })}>
        Submit KB form
      </button>
    ) : null,
}));
jest.mock('@components/llm/MemoryTable', () => ({
  MemoryTable: ({ columns, rows }) => (
    <div>
      {rows.map((row) => (
        <div key={row.id} data-testid={`row-${row.name}`}>
          {columns.map((c) => (
            <span key={c.key}>{c.render ? c.render(row) : row[c.key]}</span>
          ))}
        </div>
      ))}
    </div>
  ),
}));
jest.mock('@shared/navigation/Tabs', () => ({
  __esModule: true,
  default: ({ options, onChange }) => (
    <div>
      {options.tabOptions.map((t) => (
        <button key={t.value} type='button' onClick={() => onChange(t.value)}>
          {`tab:${t.value}`}
        </button>
      ))}
    </div>
  ),
}));
jest.mock('@ui/ThreeDotsMenu', () => ({
  __esModule: true,
  default: ({ menuItems, data, onMenuClick }) => (
    <div data-testid={`menu-${data.name}`}>
      {menuItems.map((mi) => (
        <button key={mi.value} type='button' disabled={mi.disabled} onClick={() => onMenuClick(mi, data)}>
          {`menu:${mi.label}`}
        </button>
      ))}
    </div>
  ),
}));
jest.mock('@ui/Switch', () => ({
  Switch: ({ checked, disabled, onChange, 'aria-label': ariaLabel }) => (
    <input type='checkbox' aria-label={ariaLabel} checked={checked} disabled={disabled} onChange={(e) => onChange(e, e.target.checked)} />
  ),
}));

// Tenant-wide raw list: a user KB in one account, an integration KB provisioned
// in two accounts (grouped into one row), and an integration KB in one account.
const kbs = [
  { id: 'kb-m', name: 'Runbooks', kb_type: 'manual', status: 'active', enabled: true, account_id: 'acct-9', account_name: 'Prod' },
  {
    id: 'kb-i1',
    name: 'Confluence',
    kb_type: 'integration',
    integration_id: 'int-1',
    status: 'active',
    enabled: true,
    account_id: 'acct-9',
    account_name: 'Prod',
  },
  {
    id: 'kb-i2',
    name: 'Confluence',
    kb_type: 'integration',
    integration_id: 'int-1',
    status: 'active',
    enabled: true,
    account_id: 'acct-2',
    account_name: 'Stage',
  },
  {
    id: 'kb-s',
    name: 'Jira',
    kb_type: 'integration',
    integration_id: 'int-2',
    status: 'active',
    enabled: true,
    account_id: 'acct-9',
    account_name: 'Prod',
  },
];

const ok = (data = {}) => ({ data, errors: [] });

beforeEach(() => {
  jest.clearAllMocks();
  hasWriteAccess.mockImplementation((id) => id === 'acct-9' || id === 'acct-1');
  apiKnowledgeBase.getKnowledgeBases.mockResolvedValue(ok(kbs));
  apiKnowledgeBase.getKnowledgeBase.mockResolvedValue(ok({ id: 'kb-m', name: 'Runbooks', kb_type: 'manual' }));
  apiKnowledgeBase.getKBAgents.mockResolvedValue(ok([]));
  apiKnowledgeBase.updateKnowledgeBase.mockResolvedValue(ok({ id: 'kb-m' }));
  apiKnowledgeBase.deleteKnowledgeBase.mockResolvedValue(ok());
  apiKnowledgeBase.retriggerKB.mockResolvedValue(ok());
  apiKnowledgeBase.setKnowledgeBaseEnabled.mockResolvedValue(ok());
  apiAskNudgebee.listAgents.mockResolvedValue({ data: { data: { ai_list_agents: { data: [] } } } });
  apiAskNudgebee.listTools.mockResolvedValue({ data: { data: { ai_list_tools: { data: [] } } } });
});

describe('KnowledgeBaseTab tenant-wide row actions', () => {
  it('gives a user KB in one account its actions and switch, and keeps Add hidden', async () => {
    render(<KnowledgeBaseTab accountId='' />);

    const row = await screen.findByTestId('row-Runbooks');
    expect(within(row).getByRole('button', { name: 'Edit knowledge base' })).toBeInTheDocument();
    expect(within(row).getByText('menu:Delete')).toBeEnabled();
    expect(within(row).getByRole('checkbox')).toBeEnabled();
    expect(screen.queryByRole('button', { name: 'Add Knowledge Base' })).not.toBeInTheDocument();
  });

  it('keeps an integration KB shared by several accounts read-only, but not one in a single account', async () => {
    render(<KnowledgeBaseTab accountId='' />);
    fireEvent.click(await screen.findByText('tab:integration'));

    const shared = await screen.findByTestId('row-Confluence');
    expect(within(shared).queryByTestId('menu-Confluence')).not.toBeInTheDocument();
    expect(within(shared).getByRole('checkbox')).toBeDisabled();
    const single = screen.getByTestId('row-Jira');
    expect(within(single).getByText('menu:Retrigger')).toBeEnabled();
    expect(within(single).getByRole('checkbox')).toBeEnabled();
  });

  it("runs delete, retrigger and the enable switch against the row's own account", async () => {
    render(<KnowledgeBaseTab accountId='' />);

    const row = await screen.findByTestId('row-Runbooks');
    fireEvent.click(within(row).getByRole('checkbox'));
    await waitFor(() => expect(apiKnowledgeBase.setKnowledgeBaseEnabled).toHaveBeenCalledWith('acct-9', 'kb-m', false));

    fireEvent.click(within(row).getByText('menu:Delete'));
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(apiKnowledgeBase.deleteKnowledgeBase).toHaveBeenCalledWith('acct-9', 'kb-m'));

    fireEvent.click(screen.getByText('tab:integration'));
    fireEvent.click(within(await screen.findByTestId('row-Jira')).getByText('menu:Retrigger'));
    await waitFor(() => expect(apiKnowledgeBase.retriggerKB).toHaveBeenCalledWith('acct-9', 'kb-s'));
  });

  it("loads the edit form's agents for the row's account and saves to it", async () => {
    render(<KnowledgeBaseTab accountId='' />);
    // Nothing account-scoped loads until an edit opens.
    await screen.findByTestId('row-Runbooks');
    expect(apiAskNudgebee.listAgents).not.toHaveBeenCalled();

    fireEvent.click(within(screen.getByTestId('row-Runbooks')).getByRole('button', { name: 'Edit knowledge base' }));

    await waitFor(() => expect(apiKnowledgeBase.getKnowledgeBase).toHaveBeenCalledWith('acct-9', 'kb-m'));
    expect(apiKnowledgeBase.getKBAgents).toHaveBeenCalledWith('acct-9', 'kb-m');
    await waitFor(() => expect(apiAskNudgebee.listAgents).toHaveBeenCalledWith({ accountId: 'acct-9' }));
    fireEvent.click(await screen.findByText('Submit KB form'));

    await waitFor(() =>
      expect(apiKnowledgeBase.updateKnowledgeBase).toHaveBeenCalledWith('acct-9', 'kb-m', expect.objectContaining({ name: 'Runbooks' }))
    );
  });

  it("disables a row's writes when the caller cannot write to its account", async () => {
    hasWriteAccess.mockImplementation((id) => id === 'acct-other');
    render(<KnowledgeBaseTab accountId='' />);

    const row = await screen.findByTestId('row-Runbooks');
    expect(within(row).queryByRole('button', { name: 'Edit knowledge base' })).not.toBeInTheDocument();
    expect(within(row).getByText('menu:Delete')).toBeDisabled();
    expect(within(row).getByRole('checkbox')).toBeDisabled();
  });

  it('keeps acting on the selected account when one is selected', async () => {
    render(<KnowledgeBaseTab accountId='acct-1' />);

    const row = await screen.findByTestId('row-Runbooks');
    await waitFor(() => expect(apiAskNudgebee.listAgents).toHaveBeenCalledWith({ accountId: 'acct-1' }));
    fireEvent.click(within(row).getByText('menu:Delete'));
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(apiKnowledgeBase.deleteKnowledgeBase).toHaveBeenCalledWith('acct-1', 'kb-m'));
  });
});
