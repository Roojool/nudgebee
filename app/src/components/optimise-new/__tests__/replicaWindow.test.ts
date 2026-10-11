import { replicaWindowOf, summarizeRightSizing } from '../rightSizingData';
import { buildInterpretation, formatReplicaWindow } from '../interpretation/buildInterpretation';

const entry = (resource: string, allocated: number, recommended: number, addInfo?: any) => ({
  resource,
  allocated: { request: allocated, limit: allocated },
  recommended: { request: recommended, limit: recommended },
  add_info: addInfo,
});

const replicas = { avg: 3.2, min: 2, max: 10, window_hours: 336 };

describe('replicaWindowOf', () => {
  it('reads the window off whichever entry carries it', () => {
    expect(replicaWindowOf([entry('cpu', 1, 0.5), entry('memory', 4, 2, { replicas })])).toEqual({
      avg: 3.2,
      min: 2,
      max: 10,
      windowHours: 336,
    });
  });

  it('is null for rows written before the producers recorded it', () => {
    expect(replicaWindowOf([entry('cpu', 1, 0.5), entry('memory', 4, 2)])).toBeNull();
    expect(replicaWindowOf([entry('cpu', 1, 0.5, { replicas: { avg: 'n/a' } })])).toBeNull();
  });
});

describe('formatReplicaWindow', () => {
  it('names the average, the window and the range', () => {
    expect(formatReplicaWindow({ avg: 3.2, min: 2, max: 10, windowHours: 336 })).toBe(' at an average of 3.2 replicas over 14 days (2–10)');
  });

  it('drops the range when the count never moved and the window when unknown', () => {
    expect(formatReplicaWindow({ avg: 2, min: 2, max: 2, windowHours: null })).toBe(' at an average of 2 replicas');
    expect(formatReplicaWindow({ avg: 1, min: 1, max: 1, windowHours: 168 })).toBe(' at an average of 1 replica over 7 days');
    expect(formatReplicaWindow(null)).toBe('');
  });
});

describe('pod right-sizing impact line', () => {
  const recData = { app: [entry('cpu', 1, 0.5, { replicas }), entry('memory', 4 * 1024 ** 3, 2 * 1024 ** 3, { replicas })] };

  it('says which replica count the saving assumed', () => {
    const summary = summarizeRightSizing(recData);
    expect(summary?.replicas).toEqual({ avg: 3.2, min: 2, max: 10, windowHours: 336 });
    const r = buildInterpretation({ category: 'RightSizing', ruleName: 'pod_right_sizing', savings: 42, recData } as any);
    expect(r.impact).toContain('~$42/mo savings at an average of 3.2 replicas over 14 days (2–10)');
  });

  it('keeps the old wording when the row has no replica window', () => {
    const legacy = { app: [entry('cpu', 1, 0.5)] };
    const r = buildInterpretation({ category: 'RightSizing', ruleName: 'pod_right_sizing', savings: 42, recData: legacy } as any);
    expect(r.impact).toContain('~$42/mo savings');
    expect(r.impact).not.toContain('replica');
  });
});
