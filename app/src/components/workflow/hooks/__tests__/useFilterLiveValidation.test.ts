import { renderHook, waitFor } from '@testing-library/react';
import apiWorkflow from '@api1/workflow';
import { useFilterLiveValidation } from '../useFilterLiveValidation';

jest.mock('@api1/workflow', () => ({
  __esModule: true,
  default: { checkTriggerMatch: jest.fn() },
}));

const mockCheck = (apiWorkflow as any).checkTriggerMatch as jest.Mock;
const respond = (result: Record<string, any>) => mockCheck.mockResolvedValue({ data: { workflow_check_trigger_match: result } });

const baseArgs = {
  enabled: true,
  filter: '{{ event.priority == }}',
  accountId: 'acct-own',
  triggerType: 'event',
  params: { on: 'event.created', accounts: ['acct-a'] },
  samplePayload: { cloud_account_id: 'acct-a', event_type: 'KubePodCrashLooping' },
};

describe('useFilterLiveValidation', () => {
  beforeEach(() => mockCheck.mockReset());

  it("returns the server evaluator's error for a broken expression", async () => {
    respond({ matched: false, error: "unexpected token '}}'" });

    const { result } = renderHook(() => useFilterLiveValidation(baseArgs));

    await waitFor(() => expect(result.current.error).toBe("unexpected token '}}'"));
    expect(mockCheck).toHaveBeenCalledWith({
      account_id: 'acct-own',
      trigger_type: 'event',
      params: { on: 'event.created', accounts: ['acct-a'], filter: '{{ event.priority == }}' },
      payload: baseArgs.samplePayload,
    });
  });

  it('reports nothing when the expression evaluates, matched or not', async () => {
    respond({ matched: false, gate: 'filter', reason: 'the filter evaluated to false' });

    const { result } = renderHook(() => useFilterLiveValidation(baseArgs));

    await waitFor(() => expect(mockCheck).toHaveBeenCalled());
    expect(result.current.error).toBe('');
  });

  it('stays quiet when the check itself fails', async () => {
    mockCheck.mockRejectedValue(new Error('network'));

    const { result } = renderHook(() => useFilterLiveValidation(baseArgs));

    await waitFor(() => expect(result.current.isChecking).toBe(false));
    expect(result.current.error).toBe('');
  });

  it('does not call the server when disabled or the filter is empty', async () => {
    renderHook(() => useFilterLiveValidation({ ...baseArgs, enabled: false }));
    renderHook(() => useFilterLiveValidation({ ...baseArgs, filter: '   ' }));

    await new Promise((r) => setTimeout(r, 600));
    expect(mockCheck).not.toHaveBeenCalled();
  });
});

describe('useFilterLiveValidation stale errors', () => {
  beforeEach(() => mockCheck.mockReset());

  it('drops the error as soon as the expression it named is edited', async () => {
    respond({ matched: false, error: 'parse error' });
    const { result, rerender } = renderHook((props) => useFilterLiveValidation(props), { initialProps: baseArgs });
    await waitFor(() => expect(result.current.error).toBe('parse error'));

    mockCheck.mockReturnValue(new Promise(() => {}));
    rerender({ ...baseArgs, filter: '{{ event.priority == "HIGH" }}' });

    expect(result.current.error).toBe('');
  });
});
