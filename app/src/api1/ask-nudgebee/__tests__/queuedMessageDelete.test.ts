import { queryGraphQL } from '@lib/HttpService';
import api from '../index';

jest.mock('@lib/HttpService', () => ({ queryGraphQL: jest.fn(), gqlStringify: jest.fn() }));

it('forwards a delete rejection to the UI without logging a development error overlay', async () => {
  const log = jest.spyOn(console, 'error').mockImplementation(() => {});
  try {
    (queryGraphQL as jest.Mock).mockResolvedValue({
      data: { data: { ai_delete_conversation_queued_message: null }, errors: [{ message: 'Already started' }] },
    });
    await expect(api.deleteQueuedMessage({ accountId: 'a', conversationId: 'c', messageId: 'm' })).rejects.toThrow('Already started');
    expect(log).not.toHaveBeenCalled();
  } finally {
    log.mockRestore();
  }
});
