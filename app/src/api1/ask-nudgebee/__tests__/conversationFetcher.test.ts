import { queryGraphQL } from '@lib/HttpService';
import { createConversationFetcher } from '../index';

jest.mock('@lib/HttpService', () => ({ queryGraphQL: jest.fn(), gqlStringify: jest.fn() }));
const query = queryGraphQL as jest.Mock;
const response = (status: string, messages: any[], cursor: string) => ({
  data: {
    data: {
      ai_get_conversation_v3: {
        conversation: { id: 'conv', status },
        messages,
        agents: [],
        tool_calls: [],
        cursor,
      },
    },
  },
});
const active = { id: 'message', status: 'IN_PROGRESS', message_type: 'generation' };
const opts = { accountId: 'account', conversationId: 'conv' };
let replies: any[];
beforeEach(() => {
  replies = [];
  query
    .mockReset()
    .mockImplementation((_q, op) => (op === 'AiGetConversationV3' ? Promise.resolve(replies.shift()) : Promise.resolve({ data: { data: {} } })));
});
it.each(['COMPLETED', 'IN_PROGRESS'])('reconciles a missed update while preserving actual %s state', async (messageStatus) => {
  replies.push(
    response('IN_PROGRESS', [active], 'one'),
    response('COMPLETED', [], 'two'),
    response('COMPLETED', [{ ...active, status: messageStatus }], 'three')
  );
  const fetcher = createConversationFetcher();
  await fetcher.fetch(opts);
  const result = await fetcher.fetch(opts);
  expect(result.data.data.llm_conversations[0].llm_conversation_messages[0].status).toBe(messageStatus);
  const calls = query.mock.calls.filter((c) => c[1] === 'AiGetConversationV3');
  expect(calls).toHaveLength(3);
  expect(calls[2][2].request.since).toBeUndefined();
});
it('retries reconciliation after a failed full fetch', async () => {
  replies.push(
    response('IN_PROGRESS', [active], 'one'),
    response('COMPLETED', [], 'two'),
    { data: { errors: [{ message: 'offline' }] } },
    response('COMPLETED', [], 'three'),
    response('COMPLETED', [{ ...active, status: 'COMPLETED' }], 'four')
  );
  const fetcher = createConversationFetcher();
  await fetcher.fetch(opts);
  expect((await fetcher.fetch(opts)).data.errors).toHaveLength(1);
  expect((await fetcher.fetch(opts)).data.data.llm_conversations[0].llm_conversation_messages[0].status).toBe('COMPLETED');
});
it('ignores a reconciliation response after reset and target switch', async () => {
  const fetcher = createConversationFetcher();
  replies.push(response('IN_PROGRESS', [active], 'one'));
  await fetcher.fetch(opts);
  let finishFull: (value: any) => void = () => {};
  const delayed = new Promise((resolve) => {
    finishFull = resolve;
  });
  let calls = 0;
  query.mockImplementation((_q, op) => {
    if (op !== 'AiGetConversationV3') return Promise.resolve({ data: { data: {} } });
    calls += 1;
    if (calls === 1) return Promise.resolve(response('COMPLETED', [], 'two'));
    if (calls === 2) return delayed;
    return Promise.resolve(response('COMPLETED', [], 'new'));
  });
  const pending = fetcher.fetch(opts);
  while (calls < 2) await Promise.resolve();
  fetcher.reset();
  await fetcher.fetch({ ...opts, conversationId: 'other' });
  finishFull(response('COMPLETED', [{ ...active, status: 'COMPLETED' }], 'old'));
  await pending;
  const result = await fetcher.fetch({ ...opts, conversationId: 'other' });
  expect(result.data.data.llm_conversations[0].llm_conversation_messages).toEqual([]);
});
