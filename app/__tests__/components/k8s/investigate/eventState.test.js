import { describeEventState, describeRecurredSince, formatDuration } from '@components/k8s/investigate/eventState';

const MIN = 60 * 1000;
const HOUR = 60 * MIN;
const NOW = new Date('2026-09-14T09:23:00Z').getTime();

describe('formatDuration', () => {
  it.each([
    [30 * 1000, '30s'],
    [12 * MIN, '12m'],
    [2 * HOUR + 46 * MIN, '2h 46m'],
    [3 * HOUR, '3h'],
    [(24 * 3 + 4) * HOUR, '3d 4h'],
  ])('%d ms → %s', (ms, text) => {
    expect(formatDuration(ms)).toBe(text);
  });
});

describe('describeEventState', () => {
  // The event that motivated this: an OOM kill still FIRING with no ends_at. It must not claim the
  // problem is still happening, only that no end was reported.
  it('describes FIRING as open with no end reported, not as still happening', () => {
    const s = describeEventState({ status: 'FIRING', startsAt: '2026-09-14 06:37:13', endsAt: null, now: NOW });
    expect(s.state).toBe('open');
    expect(s.label).toBe('Open');
    expect(s.detail).toBe('no end reported');
    expect(s.hint).toContain('OOM kill');
  });

  // DB timestamps carry no timezone and are UTC; a local-time parse would shift the duration.
  it('treats timezone-less timestamps as UTC', () => {
    const s = describeEventState({ status: 'CLOSED', startsAt: '2026-09-14 09:00:00', endsAt: '2026-09-14 09:13:00', now: NOW });
    expect(s.endedAgo).toBe('10m ago');
  });

  it('describes CLOSED with when it ended and how long it lasted', () => {
    const s = describeEventState({ status: 'CLOSED', startsAt: '2026-09-13T08:32:22Z', endsAt: '2026-09-13T08:33:23Z', now: NOW });
    expect(s.state).toBe('ended');
    expect(s.detail).toBe('lasted 1m');
    expect(s.hint).toMatch(/^Started .+, ended .+$/);
    expect(s.endedAgo).toBe('1d ago');
  });

  it('treats RESOLVED as ended', () => {
    expect(describeEventState({ status: 'RESOLVED', startsAt: '2026-09-14T09:00:00Z', endsAt: '2026-09-14T09:20:00Z', now: NOW }).state).toBe(
      'ended'
    );
  });

  // One-time signals are stored with ends_at == starts_at.
  it('omits the duration for a zero-length event', () => {
    const s = describeEventState({ status: 'CLOSED', startsAt: '2026-09-14T09:00:00Z', endsAt: '2026-09-14T09:00:00Z', now: NOW });
    expect(s.detail).toBe('');
    expect(s.endedAgo).toBe('23m ago');
  });

  // 9.7k PagerDuty events on dev are CLOSED with no ends_at.
  it('says the end time is not reported when an ended event has no ends_at', () => {
    const s = describeEventState({ status: 'CLOSED', startsAt: '2026-09-14T09:00:00Z', endsAt: null, now: NOW });
    expect(s.state).toBe('ended');
    expect(s.detail).toBe('end time unknown');
    expect(s.endedAgo).toBeUndefined();
  });

  it('says nothing for a missing or unknown status', () => {
    expect(describeEventState({ status: undefined, now: NOW })).toBeNull();
    expect(describeEventState({ status: 'WHATEVER', now: NOW })).toBeNull();
  });
});

describe('describeRecurredSince', () => {
  const latest = { id: 'e2', status: 'FIRING', starts_at: '2026-09-14 09:03:00' };

  it('says nothing when it never recurred', () => {
    expect(describeRecurredSince({ count: 0, ownState: 'ended', now: NOW })).toBeNull();
  });

  // The case a reader walks away from: this one is closed, the problem is back.
  it('flags an ended event whose problem came back, with the latest occurrence', () => {
    const r = describeRecurredSince({ count: 3, latest, ownState: 'ended', now: NOW });
    expect(r.cameBack).toBe(true);
    expect(r.text).toBe('3× since · last 20m ago');
    expect(r.latestOpen).toBe(true);
    expect(r.latestId).toBe('e2');
  });

  it('reports recurrences of an open event without calling it a comeback', () => {
    const r = describeRecurredSince({ count: 1, latest: { ...latest, status: 'CLOSED' }, ownState: 'open', now: NOW });
    expect(r.cameBack).toBe(false);
    expect(r.text).toBe('1× since · last 20m ago');
  });
});
