import { act, renderHook } from '@testing-library/react';
import { useOptimisticQueue } from '@hooks/useOptimisticQueue';

beforeEach(() => {
  Object.defineProperty(global.crypto, 'randomUUID', { configurable: true, value: jest.fn().mockReturnValue('request-one') });
});

it('publishes immediately, enforces pending capacity and waits for acknowledgement', async () => {
  let resolve!: () => void;
  const persist = jest.fn(
    () =>
      new Promise<void>((r) => {
        resolve = r;
      })
  );
  const { result } = renderHook(() => useOptimisticQueue('chat', 1, 0));
  act(() => {
    expect(result.current.submit('hello', [], persist)).toBe(true);
  });
  expect(result.current.entries[0]).toMatchObject({ text: 'hello', saving: true });
  act(() => {
    expect(result.current.submit('second', [], persist)).toBe(false);
  });
  await act(async () => {
    resolve();
  });
  expect(result.current.entries).toEqual([]);
});

it('retains an uncertain submission without retrying or making a sendable copy', async () => {
  const persist = jest.fn().mockRejectedValue(new Error('lost response'));
  const { result } = renderHook(() => useOptimisticQueue('chat', 5, 0));
  await act(async () => {
    result.current.submit('hello', [], persist);
  });
  expect(result.current.entries[0].saveError).toContain('refresh to check');
  expect(result.current.takeDraft('request-one')).toBeNull();
  expect(persist).toHaveBeenCalledTimes(1);
});

it('restores text and attachments only after definite non-admission', async () => {
  const images = [{ data: 'image', mime_type: 'image/png' }];
  const persist = jest.fn().mockRejectedValue(Object.assign(new Error('queue full'), { notAdmitted: true }));
  const { result } = renderHook(() => useOptimisticQueue('chat', 5, 0));
  await act(async () => {
    result.current.submit('hello', images, persist);
  });
  act(() => {
    expect(result.current.takeDraft('request-one')).toMatchObject({ text: 'hello', images });
  });
  expect(result.current.entries).toEqual([]);
});

it('does not restore a failed old-chat request after navigation', async () => {
  let reject!: (e: Error) => void;
  const { result, rerender } = renderHook(({ scope }) => useOptimisticQueue(scope, 5, 0), { initialProps: { scope: 'old' } });
  act(() => {
    result.current.submit(
      'old draft',
      [],
      () =>
        new Promise((_, r) => {
          reject = r;
        })
    );
  });
  rerender({ scope: 'new' });
  await act(async () => {
    reject(new Error('old failure'));
  });
  expect(result.current.entries).toEqual([]);
});

it('does not let uncertain submissions permanently consume queue capacity', async () => {
  const { result } = renderHook(() => useOptimisticQueue('chat', 1, 0));
  await act(async () => {
    result.current.submit('uncertain', [], jest.fn().mockRejectedValue(new Error('timeout')));
  });
  (crypto.randomUUID as jest.Mock).mockReturnValue('request-two');
  act(() => {
    expect(result.current.submit('new', [], () => new Promise(() => {}))).toBe(true);
  });
  expect(result.current.entries).toHaveLength(2);
});
