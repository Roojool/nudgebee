import { act, renderHook, waitFor } from '@testing-library/react';
import useKnowledgeBaseForm from '@components/llm/useKnowledgeBaseForm';

jest.mock('@api1/knowledge-base', () => ({
  __esModule: true,
  default: {
    getKnowledgeBase: jest.fn(),
    getKBAgents: jest.fn(),
  },
}));
jest.mock('@api1/ask-nudgebee', () => ({
  __esModule: true,
  default: { listAgents: jest.fn().mockResolvedValue({ data: {} }), listTools: jest.fn().mockResolvedValue({ data: {} }) },
}));
jest.mock('@api1/kubernetes', () => ({
  __esModule: true,
  default: { getK8sWorkloadNames: jest.fn().mockResolvedValue({ data: {} }), getK8sNamespaceNames: jest.fn().mockResolvedValue({ data: {} }) },
}));

import apiKnowledgeBase from '@api1/knowledge-base';

const mockGetKnowledgeBase = apiKnowledgeBase.getKnowledgeBase;
const mockGetKBAgents = apiKnowledgeBase.getKBAgents;

// A controllable promise: lets a test start a fetch, let a second fetch
// resolve first, then resolve this one afterward to prove the stale response
// is dropped.
const deferred = () => {
  let resolve;
  const promise = new Promise((res) => {
    resolve = res;
  });
  return { promise, resolve };
};

describe('useKnowledgeBaseForm — openEdit request-sequencing', () => {
  beforeEach(() => {
    jest.clearAllMocks();
  });

  it('keeps the second-clicked knowledge base open when the first click resolves later', async () => {
    const kb1 = deferred();
    const kb2 = deferred();
    mockGetKnowledgeBase.mockImplementation((accountId, kbId) => (kbId === 'kb1' ? kb1.promise : kb2.promise));
    mockGetKBAgents.mockResolvedValue({ data: [] });

    const { result } = renderHook(() => useKnowledgeBaseForm());

    let editKb1Promise;
    act(() => {
      editKb1Promise = result.current.openEdit('acc-1', { id: 'kb1' });
    });

    let editKb2Promise;
    act(() => {
      editKb2Promise = result.current.openEdit('acc-2', { id: 'kb2' });
    });

    await act(async () => {
      kb2.resolve({ data: { id: 'kb2', name: 'Second' } });
      await editKb2Promise;
    });

    await waitFor(() => expect(result.current.editKnowledgeBase?.id).toBe('kb2'));

    // kb1's slower response lands after kb2 already won the race — it must
    // not silently swap the open modal back to kb1's content.
    await act(async () => {
      kb1.resolve({ data: { id: 'kb1', name: 'First' } });
      await editKb1Promise;
    });

    expect(result.current.editKnowledgeBase?.id).toBe('kb2');
    expect(result.current.submitting).toBe(false);
  });

  it('does not let a stale openEdit reopen edit mode after openCreate started a fresh form', async () => {
    const kb1 = deferred();
    mockGetKnowledgeBase.mockReturnValue(kb1.promise);
    mockGetKBAgents.mockResolvedValue({ data: [] });

    const { result } = renderHook(() => useKnowledgeBaseForm());

    let editPromise;
    act(() => {
      editPromise = result.current.openEdit('acc-1', { id: 'kb1' });
    });

    act(() => {
      result.current.openCreate('acc-2');
    });

    expect(result.current.editKnowledgeBase).toBeNull();

    await act(async () => {
      kb1.resolve({ data: { id: 'kb1', name: 'First' } });
      await editPromise;
    });

    // Still create mode — the superseded edit fetch must not have written
    // kb1's data into the form the admin is now looking at.
    expect(result.current.editKnowledgeBase).toBeNull();
    expect(result.current.open).toBe(true);
  });
});
