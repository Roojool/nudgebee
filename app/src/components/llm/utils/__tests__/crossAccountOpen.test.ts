import { isPendingCrossAccountOpen } from '../crossAccountOpen';

describe('isPendingCrossAccountOpen', () => {
  const pending = { accountId: 'acc-b', sessionId: 'sess-1' };

  it('keeps the session when the switch is the one the list requested', () => {
    expect(isPendingCrossAccountOpen(pending, 'acc-b', 'sess-1')).toBe(true);
  });

  it('treats a switch to a different account as manual', () => {
    expect(isPendingCrossAccountOpen(pending, 'acc-c', 'sess-1')).toBe(false);
  });

  it('treats a switch with a different or empty session as manual (stale pending open)', () => {
    expect(isPendingCrossAccountOpen(pending, 'acc-b', 'sess-2')).toBe(false);
    expect(isPendingCrossAccountOpen(pending, 'acc-b', '')).toBe(false);
  });

  it('is false with nothing pending or no account', () => {
    expect(isPendingCrossAccountOpen(null, 'acc-b', 'sess-1')).toBe(false);
    expect(isPendingCrossAccountOpen(undefined, 'acc-b', 'sess-1')).toBe(false);
    expect(isPendingCrossAccountOpen(pending, undefined, 'sess-1')).toBe(false);
  });
});
