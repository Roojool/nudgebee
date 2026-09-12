import { renderHook, act } from '@testing-library/react';
import { useQueuedMessages } from '@hooks/useQueuedMessages';

jest.mock('@api1/ask-nudgebee', () => ({
  __esModule: true,
  default: {
    deleteQueuedMessage: jest.fn().mockResolvedValue({ data: { status: 'deleted' } }),
    forceExecuteQueuedMessage: jest.fn().mockResolvedValue({
      data: { data: { ai_execute_queued_message: { data: { status: 'executing' } } } },
    }),
  },
}));

import apiAskNudgebee from '@api1/ask-nudgebee';

describe('useQueuedMessages', () => {
  it('initializes with an empty queue and expanded state', () => {
    const onDispatch = jest.fn();
    const { result } = renderHook(() =>
      useQueuedMessages({
        isConversationInProgress: false,
        conversationStatus: '',
        onDispatch,
      })
    );

    expect(result.current.queuedMessages).toEqual([]);
    expect(result.current.isExpanded).toBe(true);
  });

  it('enqueues messages and trims text', () => {
    const onDispatch = jest.fn();
    const { result } = renderHook(() =>
      useQueuedMessages({
        isConversationInProgress: true,
        conversationStatus: 'IN_PROGRESS',
        onDispatch,
      })
    );

    act(() => {
      result.current.enqueue('  first queued question  ');
    });

    expect(result.current.queuedMessages).toHaveLength(1);
    expect(result.current.queuedMessages[0].text).toBe('first queued question');
    expect(result.current.queuedMessages[0].id).toMatch(/^queued-/);

    act(() => {
      result.current.enqueue('second question', [{ data: 'base64', mime_type: 'image/png' }]);
    });

    expect(result.current.queuedMessages).toHaveLength(2);
    expect(result.current.queuedMessages[1].text).toBe('second question');
    expect(result.current.queuedMessages[1].images).toHaveLength(1);
  });

  it('removes a message by id', () => {
    const onDispatch = jest.fn();
    const { result } = renderHook(() =>
      useQueuedMessages({
        isConversationInProgress: true,
        conversationStatus: 'IN_PROGRESS',
        onDispatch,
      })
    );

    let id1 = '';
    let id2 = '';
    act(() => {
      id1 = result.current.enqueue('message 1');
      id2 = result.current.enqueue('message 2');
    });

    expect(result.current.queuedMessages).toHaveLength(2);

    act(() => {
      result.current.remove(id1);
    });

    expect(result.current.queuedMessages).toHaveLength(1);
    expect(result.current.queuedMessages[0].id).toBe(id2);
  });

  it('edits a message by removing it and returning its payload', async () => {
    const onDispatch = jest.fn();
    const { result } = renderHook(() =>
      useQueuedMessages({
        isConversationInProgress: true,
        conversationStatus: 'IN_PROGRESS',
        onDispatch,
      })
    );

    let id = '';
    act(() => {
      id = result.current.enqueue('question to edit', [{ data: 'img', mime_type: 'image/jpeg' }]);
    });

    let edited: any = null;
    await act(async () => {
      edited = await result.current.edit(id);
    });

    expect(edited).not.toBeNull();
    expect(edited.text).toBe('question to edit');
    expect(edited.images).toEqual([{ data: 'img', mime_type: 'image/jpeg' }]);
    expect(result.current.queuedMessages).toHaveLength(0);
  });

  it('sends now immediately, calling onStop if in progress', async () => {
    const onDispatch = jest.fn();
    const onStop = jest.fn().mockResolvedValue(undefined);

    const { result } = renderHook(() =>
      useQueuedMessages({
        isConversationInProgress: true,
        conversationStatus: 'IN_PROGRESS',
        onDispatch,
        onStop,
      })
    );

    let id = '';
    act(() => {
      id = result.current.enqueue('urgent follow-up');
    });

    await act(async () => {
      await result.current.sendNow(id);
    });

    expect(onStop).toHaveBeenCalledTimes(1);
    expect(onDispatch).toHaveBeenCalledTimes(1);
    expect(onDispatch).toHaveBeenCalledWith(expect.objectContaining({ text: 'urgent follow-up' }));
    expect(result.current.queuedMessages).toHaveLength(0);
  });

  it('auto-dispatches the first queued message when turn transitions to COMPLETED', () => {
    const onDispatch = jest.fn();
    let inProgress = true;
    let status = 'IN_PROGRESS';

    const { result, rerender } = renderHook(
      ({ isProgress, convStatus }) =>
        useQueuedMessages({
          isConversationInProgress: isProgress,
          conversationStatus: convStatus,
          onDispatch,
        }),
      {
        initialProps: { isProgress: inProgress, convStatus: status },
      }
    );

    act(() => {
      result.current.enqueue('auto dispatch question');
      result.current.enqueue('second in line');
    });

    expect(result.current.queuedMessages).toHaveLength(2);
    expect(onDispatch).not.toHaveBeenCalled();

    // Turn completes
    inProgress = false;
    status = 'COMPLETED';
    rerender({ isProgress: inProgress, convStatus: status });

    expect(onDispatch).toHaveBeenCalledTimes(1);
    expect(onDispatch).toHaveBeenCalledWith(expect.objectContaining({ text: 'auto dispatch question' }));
    expect(result.current.queuedMessages).toHaveLength(1);
    expect(result.current.queuedMessages[0].text).toBe('second in line');
  });

  it('does NOT auto-dispatch if turn finishes in FAILED or TERMINATED state', () => {
    const onDispatch = jest.fn();
    let inProgress = true;
    let status = 'IN_PROGRESS';

    const { result, rerender } = renderHook(
      ({ isProgress, convStatus }) =>
        useQueuedMessages({
          isConversationInProgress: isProgress,
          conversationStatus: convStatus,
          onDispatch,
        }),
      {
        initialProps: { isProgress: inProgress, convStatus: status },
      }
    );

    act(() => {
      result.current.enqueue('will stay queued on error');
    });

    inProgress = false;
    status = 'FAILED';
    rerender({ isProgress: inProgress, convStatus: status });

    expect(onDispatch).not.toHaveBeenCalled();
    expect(result.current.queuedMessages).toHaveLength(1);
  });

  it('toggles and sets expanded state', () => {
    const onDispatch = jest.fn();
    const { result } = renderHook(() =>
      useQueuedMessages({
        isConversationInProgress: false,
        conversationStatus: '',
        onDispatch,
      })
    );

    expect(result.current.isExpanded).toBe(true);

    act(() => {
      result.current.toggleExpanded();
    });
    expect(result.current.isExpanded).toBe(false);

    act(() => {
      result.current.setIsExpanded(true);
    });
    expect(result.current.isExpanded).toBe(true);
  });

  it('clears all messages', () => {
    const onDispatch = jest.fn();
    const { result } = renderHook(() =>
      useQueuedMessages({
        isConversationInProgress: false,
        conversationStatus: '',
        onDispatch,
      })
    );

    act(() => {
      result.current.enqueue('msg 1');
      result.current.enqueue('msg 2');
    });
    expect(result.current.queuedMessages).toHaveLength(2);

    act(() => {
      result.current.clear();
    });
    expect(result.current.queuedMessages).toHaveLength(0);
  });

  it('does not conflate distinct messages with identical text', () => {
    const onDispatch = jest.fn();
    const serverMsgs = [
      {
        id: 'server-msg-1',
        text: 'server confirmed question',
        createdAt: 1000,
        user: 'Bob',
        userId: 'user-bob',
        isServer: true,
      },
    ];

    const { result, rerender } = renderHook(
      ({ serverQueuedMessages }) =>
        useQueuedMessages({
          isConversationInProgress: true,
          conversationStatus: 'IN_PROGRESS',
          conversationId: 'conv-123',
          accountId: 'acc-123',
          serverQueuedMessages,
          onDispatch,
        }),
      {
        initialProps: { serverQueuedMessages: [] as any[] },
      }
    );

    // Optimistically add an item
    act(() => {
      result.current.enqueue('optimistic question');
    });
    expect(result.current.queuedMessages).toHaveLength(1);
    expect(result.current.queuedMessages[0].text).toBe('optimistic question');

    // Server poll returns server message plus the confirmed optimistic question
    rerender({
      serverQueuedMessages: [
        ...serverMsgs,
        {
          id: 'server-msg-2',
          text: 'optimistic question',
          createdAt: 2000,
          user: 'You',
          userId: 'user-self',
          isServer: true,
        },
      ],
    });

    // Identical text is not an identity: preserve all three messages.
    expect(result.current.queuedMessages).toHaveLength(3);
    expect(result.current.queuedMessages[0].id).toBe('server-msg-1');
    expect(result.current.queuedMessages[1].id).toBe('server-msg-2');
  });

  it('deletes a server queued message via deleteQueuedMessage', async () => {
    const onDispatch = jest.fn();
    const serverMsgs = [
      {
        id: 'server-msg-1',
        text: 'to delete',
        createdAt: 1000,
        user: 'You',
        userId: 'user-self',
        isServer: true,
      },
    ];

    const { result } = renderHook(() =>
      useQueuedMessages({
        isConversationInProgress: true,
        conversationStatus: 'IN_PROGRESS',
        conversationId: 'conv-123',
        accountId: 'acc-123',
        serverQueuedMessages: serverMsgs,
        onDispatch,
      })
    );

    await act(async () => {
      await result.current.remove('server-msg-1');
    });

    expect(apiAskNudgebee.deleteQueuedMessage).toHaveBeenCalledWith({
      accountId: 'acc-123',
      conversationId: 'conv-123',
      messageId: 'server-msg-1',
    });
  });

  it('edits a server queued message via deleteQueuedMessage and returns its content', async () => {
    const onDispatch = jest.fn();
    const serverMsgs = [
      {
        id: 'server-msg-edit',
        text: 'edit me',
        createdAt: 1000,
        user: 'You',
        userId: 'user-self',
        isServer: true,
      },
    ];

    const { result } = renderHook(() =>
      useQueuedMessages({
        isConversationInProgress: true,
        conversationStatus: 'IN_PROGRESS',
        conversationId: 'conv-123',
        accountId: 'acc-123',
        serverQueuedMessages: serverMsgs,
        onDispatch,
      })
    );

    let edited: any = null;
    await act(async () => {
      edited = await result.current.edit('server-msg-edit');
    });

    expect(edited).not.toBeNull();
    expect(edited.text).toBe('edit me');
    expect(apiAskNudgebee.deleteQueuedMessage).toHaveBeenCalledWith({
      accountId: 'acc-123',
      conversationId: 'conv-123',
      messageId: 'server-msg-edit',
    });
  });

  it('forces execution of a server queued message via forceExecuteQueuedMessage', async () => {
    const onDispatch = jest.fn();
    const serverMsgs = [
      {
        id: 'server-msg-force',
        text: 'run right now',
        createdAt: 1000,
        user: 'You',
        userId: 'user-self',
        isServer: true,
      },
    ];

    const { result } = renderHook(() =>
      useQueuedMessages({
        isConversationInProgress: true,
        conversationStatus: 'IN_PROGRESS',
        conversationId: 'conv-123',
        accountId: 'acc-123',
        serverQueuedMessages: serverMsgs,
        onDispatch,
      })
    );

    await act(async () => {
      await result.current.sendNow('server-msg-force');
    });

    expect(apiAskNudgebee.forceExecuteQueuedMessage).toHaveBeenCalledWith({
      accountId: 'acc-123',
      conversationId: 'conv-123',
      messageId: 'server-msg-force',
    });
    // Since server successfully responded executing, local onDispatch is not needed
    expect(onDispatch).not.toHaveBeenCalled();
  });

  it('respects maxQueuedMessages limit and sets isMaxQueueReached', () => {
    const onDispatch = jest.fn();
    const { result } = renderHook(() =>
      useQueuedMessages({
        isConversationInProgress: true,
        conversationStatus: 'IN_PROGRESS',
        maxQueuedMessages: 2,
        onDispatch,
      })
    );

    expect(result.current.maxQueuedMessages).toBe(2);
    expect(result.current.isMaxQueueReached).toBe(false);

    let id1 = '';
    let id2 = '';
    let id3 = '';

    act(() => {
      id1 = result.current.enqueue('msg 1');
    });
    expect(id1).toBeTruthy();
    expect(result.current.queuedMessages).toHaveLength(1);
    expect(result.current.isMaxQueueReached).toBe(false);

    act(() => {
      id2 = result.current.enqueue('msg 2');
    });
    expect(id2).toBeTruthy();
    expect(result.current.queuedMessages).toHaveLength(2);
    expect(result.current.isMaxQueueReached).toBe(true);

    // Attempting to enqueue beyond limit returns empty string and does not add message
    act(() => {
      id3 = result.current.enqueue('msg 3');
    });
    expect(id3).toBe('');
    expect(result.current.queuedMessages).toHaveLength(2);
    expect(result.current.isMaxQueueReached).toBe(true);

    // Removing a message frees up the queue
    act(() => {
      result.current.remove(id1);
    });
    expect(result.current.queuedMessages).toHaveLength(1);
    expect(result.current.isMaxQueueReached).toBe(false);

    // Now an enqueue can succeed again
    act(() => {
      id3 = result.current.enqueue('msg 3');
    });
    expect(id3).toBeTruthy();
    expect(result.current.queuedMessages).toHaveLength(2);
    expect(result.current.isMaxQueueReached).toBe(true);
  });
  it('keeps a server message when edit loses the race with dispatch', async () => {
    (apiAskNudgebee.deleteQueuedMessage as jest.Mock).mockRejectedValueOnce(new Error('Already started'));
    const msg = { id: 'queued-1', text: 'keep me', createdAt: 1, isServer: true };
    const { result } = renderHook(() =>
      useQueuedMessages({
        accountId: 'a',
        conversationId: 'c',
        isConversationInProgress: true,
        conversationStatus: 'IN_PROGRESS',
        serverQueuedMessages: [msg],
        onDispatch: jest.fn(),
      })
    );
    let edited;
    await act(async () => {
      edited = await result.current.edit(msg.id);
    });
    expect(edited).toBeNull();
    expect(result.current.queuedMessages).toEqual([msg]);
    expect(result.current.error).toBe('Already started');
  });

  it('never falls back to a second execution when server Send Now fails', async () => {
    (apiAskNudgebee.forceExecuteQueuedMessage as jest.Mock).mockRejectedValueOnce(new Error('Forbidden'));
    const onDispatch = jest.fn();
    const onStop = jest.fn();
    const msg = { id: 'queued-1', text: 'keep me', createdAt: 1, isServer: true };
    const { result } = renderHook(() =>
      useQueuedMessages({
        accountId: 'a',
        conversationId: 'c',
        isConversationInProgress: true,
        conversationStatus: 'IN_PROGRESS',
        serverQueuedMessages: [msg],
        onDispatch,
        onStop,
      })
    );
    await act(async () => {
      await result.current.sendNow(msg.id);
    });
    expect(onDispatch).not.toHaveBeenCalled();
    expect(onStop).not.toHaveBeenCalled();
    expect(result.current.queuedMessages).toEqual([msg]);
    expect(result.current.error).toBe('Forbidden');
  });

  it('does not auto-dispatch server messages when the preceding turn completes', async () => {
    const onDispatch = jest.fn();
    const serverQueuedMessages = [{ id: 'queued-1', text: 'one', createdAt: 1, isServer: true }];
    const { rerender } = renderHook(
      ({ running }) =>
        useQueuedMessages({
          accountId: 'a',
          conversationId: 'c',
          isConversationInProgress: running,
          conversationStatus: running ? 'IN_PROGRESS' : 'COMPLETED',
          serverQueuedMessages,
          onDispatch,
        }),
      { initialProps: { running: true } }
    );
    await act(async () => {
      rerender({ running: false });
    });
    expect(onDispatch).not.toHaveBeenCalled();
  });
});
