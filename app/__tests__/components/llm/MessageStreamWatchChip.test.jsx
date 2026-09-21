import React from 'react';
import { act, render } from '@testing-library/react';
import MessageStream from '@components/llm/MessageStream';
import api from '@api1/ask-nudgebee';

// Regression cover for #38881: the Watch chip did not appear until the page was
// manually refreshed.
//
// The chip is driven by MessageStream's watch poller, which stands down for good
// once the chat is settled and there is no live watch (MessageStream.jsx:176).
// It only re-arms when one of its effect deps changes — conversationId,
// watchFeatureEnabled, or `isProcessing`.
//
// `isProcessing` is the parent's `isConversationInProgress`, which is
// ACTIVE_EXECUTION_STATUSES-only ({IN_PROGRESS, WAITING_FOR_CLIENT_TOOL}) and is
// therefore FALSE while a conversation sits in WAITING on a tool confirmation —
// which is the state every mutating, watch-registering turn passes through.

jest.mock('@api1/ask-nudgebee', () => ({ __esModule: true, default: { listWatchesByConversation: jest.fn() } }));
jest.mock('@hooks/useTenantBranding', () => ({ useWatchFeatureEnabled: () => true }));
jest.mock('@hooks/useMessageAdditionalData', () => ({ __esModule: true, default: () => ({}) }));
jest.mock('@components/llm/MessageItem', () => ({ __esModule: true, default: () => null }));
jest.mock('@components/llm/WatchesTab', () => ({ __esModule: true, default: () => null }));
jest.mock('@shared/CustomDrawer', () => ({ __esModule: true, default: () => null, SecondaryDrawer: () => null }));
jest.mock('@components/llm/common/TasksDrawerContent', () => ({ __esModule: true, default: () => null }));
jest.mock('@components/llm/common/MemoriesDrawerContent', () => ({ __esModule: true, default: () => null }));
jest.mock('@components/llm/common/ReferencesDrawerContent', () => ({ __esModule: true, default: () => null }));
jest.mock('@components/llm/common/ChannelContextDrawerContent', () => ({ __esModule: true, default: () => null }));
jest.mock('@components/llm/common/ToolDetails', () => ({ __esModule: true, default: () => null }));

const CID = 'conv-1';
const WATCH = { id: 'w-1', status: 'PENDING' };

const settle = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};
const advance = async (ms) => {
  await act(async () => {
    jest.advanceTimersByTime(ms);
    await Promise.resolve();
    await Promise.resolve();
  });
};

const renderStream = (props = {}) =>
  render(
    <MessageStream
      messages={[{ type: 'response', text: 'Restarting.' }]}
      isProcessing={props.isProcessing ?? false}
      isBusy={props.isBusy}
      collapsedObj={{}}
      setCollapsedObj={jest.fn()}
      showFullText={{}}
      setShowFullText={jest.fn()}
      itemProps={{ accountId: 'acct-1', conversationId: CID, onWatchTerminal: jest.fn() }}
    />
  );

beforeEach(() => {
  jest.useFakeTimers();
  api.listWatchesByConversation.mockReset();
});
afterEach(() => jest.useRealTimers());

it('re-arms the poller when the chat enters WAITING, so a watch registered behind a tool confirmation shows up without a refresh', async () => {
  // 1. Settled chat, no watches → the poller stands down (by design).
  api.listWatchesByConversation.mockResolvedValue([]);
  const { rerender } = renderStream({ isProcessing: false, isBusy: false });
  await settle();
  expect(api.listWatchesByConversation).toHaveBeenCalledTimes(1);

  // 2. The user asks for an action. The conversation goes to WAITING on the tool
  //    confirmation: isProcessing stays FALSE (WAITING is not an active-execution
  //    status) but isSystemBusy is TRUE. That is the signal the poller now uses.
  api.listWatchesByConversation.mockResolvedValue([WATCH]);
  rerender(
    <MessageStream
      messages={[{ type: 'response', text: 'Restarting.' }]}
      isProcessing={false}
      isBusy
      collapsedObj={{}}
      setCollapsedObj={jest.fn()}
      showFullText={{}}
      setShowFullText={jest.fn()}
      itemProps={{ accountId: 'acct-1', conversationId: CID, onWatchTerminal: jest.fn() }}
    />
  );
  await settle();

  // The dep changed, so the poller re-armed and fetched immediately — no refresh.
  expect(api.listWatchesByConversation).toHaveBeenCalledTimes(2);

  // 3. And it keeps ticking while the watch is live, so status changes flow in.
  await advance(5000);
  expect(api.listWatchesByConversation).toHaveBeenCalledTimes(3);
});

it('still stands down on a settled chat with no live watch — the infinite-poll guard is intact', async () => {
  api.listWatchesByConversation.mockResolvedValue([]);
  renderStream({ isProcessing: false, isBusy: false });
  await settle();
  expect(api.listWatchesByConversation).toHaveBeenCalledTimes(1);

  await advance(60 * 1000);
  expect(api.listWatchesByConversation).toHaveBeenCalledTimes(1);
});

it('falls back to isProcessing when isBusy is not supplied', async () => {
  // Guards the `isBusy ?? isProcessing` default so a caller that has not been
  // updated keeps the old behaviour rather than silently never polling.
  api.listWatchesByConversation.mockResolvedValue([]);
  renderStream({ isProcessing: true });
  await settle();
  expect(api.listWatchesByConversation).toHaveBeenCalledTimes(1);

  await advance(5000);
  expect(api.listWatchesByConversation).toHaveBeenCalledTimes(2);
});
