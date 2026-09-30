import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import GlobalContextTab from '@components/llm/GlobalContextTab';
import apiGlobalContext from '@api1/global-context';
import { hasWriteAccess } from '@lib/auth';

jest.mock('@api1/global-context', () => ({
  __esModule: true,
  default: {
    getGlobalContexts: jest.fn(),
    getGlobalContext: jest.fn(),
    createGlobalContext: jest.fn(),
    updateGlobalContext: jest.fn(),
    deleteGlobalContext: jest.fn(),
  },
}));
jest.mock('@lib/auth', () => ({ hasWriteAccess: jest.fn() }));
jest.mock('@components/llm/ScopeChip', () => ({ __esModule: true, default: () => null, HybridScopeChips: () => null }));
jest.mock('@ui/ThreeDotsMenu', () => ({
  __esModule: true,
  default: ({ menuItems, data, onMenuClick }) => (
    <div data-testid={`menu-${data.id}`}>
      {menuItems.map((mi) => (
        <button key={mi.value} disabled={mi.disabled} onClick={() => onMenuClick(mi, data)}>
          {`menu:${mi.label}`}
        </button>
      ))}
    </div>
  ),
}));

const contexts = [
  { id: 'c1', name: 'Prod rules', account_id: 'acct-9', account_name: 'Prod' },
  { id: 'c2', name: 'Stage rules', account_id: 'acct-2', account_name: 'Stage' },
];

beforeEach(() => {
  jest.clearAllMocks();
  hasWriteAccess.mockImplementation((id) => id === 'acct-9' || id === 'acct-1');
  apiGlobalContext.getGlobalContexts.mockResolvedValue({ data: contexts });
  apiGlobalContext.getGlobalContext.mockResolvedValue({ data: { id: 'c1', name: 'Prod rules', content: 'Be terse.' } });
  apiGlobalContext.updateGlobalContext.mockResolvedValue({ data: { id: 'c1' } });
  apiGlobalContext.deleteGlobalContext.mockResolvedValue({ data: { ok: true } });
});

describe('GlobalContextTab tenant-wide card actions', () => {
  it('enables Edit / Delete only on cards whose account the caller can write to, and keeps Add hidden', async () => {
    render(<GlobalContextTab accountId='' />);

    const writable = await screen.findByTestId('menu-c1');
    expect(within(writable).getByText('menu:Edit')).toBeEnabled();
    expect(within(writable).getByText('menu:Delete')).toBeEnabled();
    expect(within(screen.getByTestId('menu-c2')).getByText('menu:Edit')).toBeDisabled();
    expect(screen.queryByRole('button', { name: /Add Account Context/ })).not.toBeInTheDocument();
  });

  it("deletes a card against the card's own account", async () => {
    render(<GlobalContextTab accountId='' />);

    fireEvent.click(within(await screen.findByTestId('menu-c1')).getByText('menu:Delete'));
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(apiGlobalContext.deleteGlobalContext).toHaveBeenCalledWith('acct-9', 'c1'));
  });

  it("loads and saves an edit against the card's own account", async () => {
    render(<GlobalContextTab accountId='' />);

    fireEvent.click(within(await screen.findByTestId('menu-c1')).getByText('menu:Edit'));
    await waitFor(() => expect(apiGlobalContext.getGlobalContext).toHaveBeenCalledWith('acct-9', 'c1'));
    fireEvent.click(await screen.findByRole('button', { name: 'Update' }));

    await waitFor(() =>
      expect(apiGlobalContext.updateGlobalContext).toHaveBeenCalledWith('acct-9', 'c1', expect.objectContaining({ name: 'Prod rules' }))
    );
  });

  it('keeps acting on the selected account when one is selected', async () => {
    render(<GlobalContextTab accountId='acct-1' />);

    fireEvent.click(within(await screen.findByTestId('menu-c1')).getByText('menu:Delete'));
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(apiGlobalContext.deleteGlobalContext).toHaveBeenCalledWith('acct-1', 'c1'));
  });
});
