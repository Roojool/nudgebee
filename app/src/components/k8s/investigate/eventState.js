// Whether an event is still going on, has ended, or came back.
//
// `status` is the source's lifecycle, not the triage status: FIRING until the source reports an end,
// then CLOSED (the source ended it, or the inactive-resource sweep closed it) or RESOLVED (a
// remediation applied, or a source that says "resolved"). Both of the latter mean "not firing".
//
// FIRING is not the same as "still happening". One-time signals — an OOM kill, a crash — are never
// reported as ended by their source; they sit in FIRING until something closes them. So an open
// event is described as "no end reported", and whether the problem is actually still occurring is
// answered by recurrence: newer events with the same fingerprint.

import { formatTooltip, parseDateValue } from '@shared/format/Datetime';

const SEC = 1000;
const MIN = 60 * SEC;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

const ENDED_STATUSES = ['CLOSED', 'RESOLVED'];

export const OPEN_EVENT_HINT =
  'The source has not reported this as ended. One-time signals such as an OOM kill or a crash are never reported as ended and stay open until they are closed automatically, so check whether it recurred to know if it is still happening.';

/** Compact duration: 45s, 12m, 2h 46m, 3d 4h. */
export function formatDuration(ms) {
  if (!Number.isFinite(ms) || ms < 0) return '';
  if (ms < MIN) return `${Math.max(1, Math.round(ms / SEC))}s`;
  if (ms < HOUR) return `${Math.floor(ms / MIN)}m`;
  if (ms < DAY) {
    const h = Math.floor(ms / HOUR);
    const m = Math.floor((ms % HOUR) / MIN);
    return m ? `${h}h ${m}m` : `${h}h`;
  }
  const d = Math.floor(ms / DAY);
  const h = Math.floor((ms % DAY) / HOUR);
  return h ? `${d}d ${h}h` : `${d}d`;
}

// Same "HH:MM TZ, DD-Mon-YYYY" form as the app's other timestamp tooltips.
const formatClock = (ms) => formatTooltip(new Date(ms));

const toMs = (value) => {
  if (!value) return NaN;
  return parseDateValue(value).getTime();
};

/**
 * Describe the event's own lifecycle state.
 *
 * @returns {null | { state: 'open'|'ended', label: string, tone: string, detail: string, hint?: string, endedAgo?: string }}
 *   null when the status is missing or unknown — say nothing rather than guess.
 */
export function describeEventState({ status, startsAt, endsAt, now = Date.now() }) {
  const s = String(status || '').toUpperCase();
  const startMs = toMs(startsAt);
  const endMs = toMs(endsAt);

  // The start time is already on the "When" row, so the detail only adds what that row can't say.
  if (s === 'FIRING') {
    return { state: 'open', label: 'Open', tone: 'critical', detail: 'no end reported', hint: OPEN_EVENT_HINT };
  }

  if (ENDED_STATUSES.includes(s)) {
    if (!Number.isFinite(endMs)) {
      return { state: 'ended', label: 'Ended', tone: 'success', detail: 'end time unknown' };
    }
    const endedAgo = `${formatDuration(now - endMs)} ago`;
    // A one-time signal is stored with ends_at == starts_at; "lasted 1s" would be noise.
    const hasDuration = Number.isFinite(startMs) && endMs - startMs >= SEC;
    const detail = hasDuration ? `lasted ${formatDuration(endMs - startMs)}` : '';
    // Exact times behind the relative ones, so "lasted 5m" can be checked against the alert.
    const hint = hasDuration ? `Started ${formatClock(startMs)}, ended ${formatClock(endMs)}` : `Ended ${formatClock(endMs)}`;
    return { state: 'ended', label: 'Ended', tone: 'success', detail, endedAgo, hint };
  }

  return null;
}

/**
 * Describe newer occurrences of the same problem.
 *
 * @param {object} input
 * @param {number} input.count      events with the same fingerprint created after this one
 * @param {object} [input.latest]   the newest of them: { id, status, starts_at }
 * @param {string} input.ownState   this event's state from describeEventState ('open' | 'ended')
 * @returns {null | { tone: string, text: string, latestId?: string, latestOpen: boolean, cameBack: boolean }}
 */
export function describeRecurredSince({ count, latest, ownState, now = Date.now() }) {
  if (!count || count <= 0) return null;
  const latestStartMs = toMs(latest?.starts_at);
  const latestOpen = String(latest?.status || '').toUpperCase() === 'FIRING';
  // Short enough to fit the sidebar's value column on one line.
  const parts = [`${count}× since`];
  if (Number.isFinite(latestStartMs)) {
    parts.push(`last ${formatDuration(now - latestStartMs)} ago`);
  }
  return {
    // An ended event whose problem came back is the case a reader would otherwise walk away from.
    cameBack: ownState === 'ended',
    tone: 'warning',
    text: parts.join(' · '),
    latestId: latest?.id ? String(latest.id) : undefined,
    latestOpen,
  };
}
