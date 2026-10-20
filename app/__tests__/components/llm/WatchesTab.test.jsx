import { describeWatchTarget, describeWatchOutcome, buildHoverDetails } from '@components/llm/WatchesTab';

// #38901: the panel described the poller, not the task — UUID headline,
// "1 polls", red "1 failed" on a healthy retry.

jest.mock('@api1/ask-nudgebee', () => ({ __esModule: true, default: { listWatchesByConversation: jest.fn(), cancelWatch: jest.fn() } }));
jest.mock('@hooks/useTenantBranding', () => ({ useWatchFeatureEnabled: () => true }));
jest.mock('@shared/snackbarService', () => ({ snackbar: jest.fn() }));

describe('describeWatchTarget', () => {
  it('shows the command the backend says this polls', () => {
    expect(describeWatchTarget({ watch_target: 'gh run view 35580493474 --repo nudgebee/nudgebee-infra' })).toBe(
      'gh run view 35580493474 --repo nudgebee/nudgebee…'
    );
    expect(describeWatchTarget({ watch_target: 'kubectl rollout status deploy/shipping -n demo' })).toBe(
      'kubectl rollout status deploy/shipping -n demo'
    );
  });

  it('truncates rather than overflowing the row', () => {
    const out = describeWatchTarget({ watch_target: 'x'.repeat(200) });
    expect(out).toHaveLength(49);
    expect(out.endsWith('…')).toBe(true);
  });

  // watch_target is empty for a sql source, and absent on an older backend.
  it('falls back to the condition, then to a noun', () => {
    expect(describeWatchTarget({ watch_target: '', predicate_expr: 'successfully rolled out' })).toBe('for “successfully rolled out”');
    expect(describeWatchTarget({ source_kind: 'sql', watch_target: '' })).toBe('a database query');
    expect(describeWatchTarget({ source_kind: 'tool' })).toBe('a command');
    expect(describeWatchTarget(undefined)).toBe('a command');
  });

  it('ignores a whitespace-only target', () => {
    expect(describeWatchTarget({ watch_target: '   ', predicate_expr: 'done' })).toBe('for “done”');
  });
});

describe('describeWatchOutcome', () => {
  it('says what a completed watch matched', () => {
    expect(describeWatchOutcome({ status: 'COMPLETED', predicate_expr: 'completed' })).toBe('matched “completed”');
  });

  it('distinguishes a timeout from a broken check', () => {
    expect(describeWatchOutcome({ status: 'EXPIRED' })).toBe('gave up before the condition was met');
    expect(describeWatchOutcome({ status: 'FAILED' })).toBe('the check could not run');
  });

  // The pill already reads "Cancelled"; returning a word here rendered
  // "Cancelled · cancelled · 3m ago".
  it('stays silent when the pill already says it', () => {
    expect(describeWatchOutcome({ status: 'CANCELLED' })).toBe('');
    expect(describeWatchOutcome({ status: 'PENDING' })).toBe('');
    expect(describeWatchOutcome({ status: 'ACTIVE' })).toBe('');
  });
});

describe('buildHoverDetails', () => {
  it('pluralizes a single check — the bug that started this', () => {
    const out = buildHoverDetails({ id: 'abc', poll_count: 1, poll_interval_sec: 30, failure_count: 0 });
    expect(out).toContain('1 check so far, every 30s');
    expect(out).not.toContain('1 checks');
  });

  it('labels transient failures as retried rather than broken', () => {
    expect(buildHoverDetails({ id: 'abc', poll_count: 3, poll_interval_sec: 30, failure_count: 1 })).toContain('1 transient failure (retried)');
  });
});
