// The Ask page chat list spans every account the user can see.

import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import ConversationList from '@components/llm/ConversationListV2';
import apiAskNudgebee from '@api1/ask-nudgebee';

const mockRouter = { query: {}, isReady: true, pathname: '/ask-nudgebee', asPath: '/ask-nudgebee', push: jest.fn(), replace: jest.fn() };
jest.mock('next/router', () => ({ useRouter: () => mockRouter }));
jest.mock('@lib/auth', () => ({ getUserSession: () => ({ user: { email: 'me@example.com' } }) }));
jest.mock('@context/DataContext', () => ({
  useData: () => ({
    allCluster: [
      { value: 'acc-a', label: 'prod-eks', cloud_provider: 'K8S' },
      { value: 'acc-b', label: 'payments-gke', cloud_provider: 'GCP' },
      { value: 'demo', label: 'Demo', cloud_provider: 'K8S' },
    ],
  }),
}));
jest.mock('@api1/ask-nudgebee', () => ({ __esModule: true, default: { llmConversationHistory: jest.fn(), deleteConversation: jest.fn() } }));

const row = { id: 'c1', session_id: 's1', title: 'OOMKilled pods', status: 'COMPLETED', account_id: 'acc-b', user: {}, for_status: [] };

const renderList = (props = {}) =>
  render(
    <ConversationList
      onSelectConversation={jest.fn()}
      isConversationListVisible
      triggerHandleNewChat={jest.fn()}
      likedConversations={[]}
      setLikedConversations={jest.fn()}
      rawConversations={[row]}
      setRawConversations={jest.fn()}
      setSelectedConversation={jest.fn()}
      {...props}
    />
  );

const listQueries = () => apiAskNudgebee.llmConversationHistory.mock.calls.map(([q]) => q).filter((q) => q.limit !== 1);

describe('ConversationListV2 across accounts', () => {
  beforeEach(() => {
    mockRouter.query = {};
    apiAskNudgebee.llmConversationHistory.mockReset();
    apiAskNudgebee.llmConversationHistory.mockResolvedValue({ data: { data: { llm_conversations: [] } } });
  });

  it('queries every real account by id, never the synthetic demo account, when no account filter is set', async () => {
    renderList();
    await waitFor(() => expect(listQueries().length).toBeGreaterThan(0));
    expect(listQueries()[0].account_id).toEqual(['acc-a', 'acc-b']);
  });

  it('queries only the accounts in ?accountIds= and hides the per-row account for a single account', async () => {
    mockRouter.query = { accountIds: 'acc-b' };
    renderList();
    await waitFor(() => expect(listQueries().length).toBeGreaterThan(0));
    expect(listQueries()[0].account_id).toEqual(['acc-b']);
    expect(screen.queryByTestId('conv-row-account')).toBeNull();
  });

  it('labels each row with its account and hands that account to the opener', async () => {
    const onSelectConversation = jest.fn();
    renderList({ onSelectConversation });
    await waitFor(() => expect(listQueries().length).toBeGreaterThan(0));
    expect(screen.getByTestId('conv-row-account').textContent).toContain('payments-gke');
    fireEvent.click(screen.getByText('OOMKilled pods'));
    expect(onSelectConversation).toHaveBeenCalledWith('s1', '-', 'acc-b');
  });
});
