import { act, renderHook } from '@testing-library/react';
import { dismissMenuEntry, reactivateRecommendations, useRecommendationDismissal } from '../dismissal';

jest.mock('@api1/recommendation', () => ({
  __esModule: true,
  default: { updateRecommendationDismissal: jest.fn() },
}));
jest.mock('@ui/Toast', () => ({ toast: { success: jest.fn(), error: jest.fn(), warning: jest.fn(), info: jest.fn() } }));
jest.mock('@components/optimise-new/DismissModal', () => ({ __esModule: true, default: () => null }));

import recommendationApi from '@api1/recommendation';

const update = recommendationApi.updateRecommendationDismissal as jest.Mock;

describe('dismissMenuEntry', () => {
  it('offers dismiss from Open and reactivate from Dismissed', () => {
    expect(dismissMenuEntry('Open', true)).toEqual({ id: 'dismiss', label: 'Dismiss / snooze' });
    expect(dismissMenuEntry(undefined, true)).toEqual({ id: 'dismiss', label: 'Dismiss / snooze' });
    expect(dismissMenuEntry('Dismissed', true)).toEqual({ id: 'reactivate', label: 'Reactivate' });
  });

  it('offers nothing the backend would refuse, or without write access', () => {
    expect(dismissMenuEntry('InProgress', true)).toBeNull();
    expect(dismissMenuEntry('Closed', true)).toBeNull();
    expect(dismissMenuEntry('Archive', true)).toBeNull();
    expect(dismissMenuEntry('Open', false)).toBeNull();
  });
});

describe('reactivateRecommendations', () => {
  beforeEach(() => update.mockReset());

  it('counts only changes the backend confirmed', async () => {
    update
      .mockResolvedValueOnce({ data: { applied: true } })
      .mockResolvedValueOnce({ data: { applied: false, message: 'not dismissed' } })
      .mockResolvedValueOnce({ errors: [{ message: 'denied' }] })
      .mockRejectedValueOnce(new Error('network'))
      .mockResolvedValueOnce({ data: null });
    const recs = ['a', 'b', 'c', 'd', 'e'].map((id) => ({ id, account_id: 'acc', status: 'Dismissed' }));
    await expect(reactivateRecommendations(recs)).resolves.toEqual({ applied: 1, failed: 4 });
    expect(update).toHaveBeenCalledTimes(5);
    expect(update).toHaveBeenCalledWith('acc', 'a', { dismissed: false });
  });

  it('works through the rows in batches', async () => {
    update.mockResolvedValue({ data: { applied: true } });
    const recs = Array.from({ length: 23 }, (_, i) => ({ id: String(i), account_id: 'acc' }));
    await expect(reactivateRecommendations(recs)).resolves.toEqual({ applied: 23, failed: 0 });
    expect(update).toHaveBeenCalledTimes(23);
  });
});

describe('useRecommendationDismissal input shapes', () => {
  it('accepts a plain array of rows as a target', async () => {
    update.mockReset();
    update.mockResolvedValue({ data: { applied: true } });
    const onChanged = jest.fn();
    const { result } = renderHook(() => useRecommendationDismissal(onChanged));
    await act(async () => {
      await result.current.reactivate([
        { id: 'a', account_id: 'acc', status: 'Dismissed' },
        { id: 'b', account_id: 'acc', status: 'Dismissed' },
      ]);
    });
    expect(update).toHaveBeenCalledTimes(2);
    expect(onChanged).toHaveBeenCalledTimes(1);
  });
});
