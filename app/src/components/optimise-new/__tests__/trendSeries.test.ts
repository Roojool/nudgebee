import { buildTrendSeries } from '../evidence/trendSeries';
import type { PerPodTrend } from '../evidence/perPodTrend';

const MB = 1024 * 1024;

const trend = (rows: any[], hasBusiestPod = false): PerPodTrend => ({ rows, source: 'measured', replicas: 2, hasBusiestPod });

const row = (ts: string, cpu: number, memBytes: number, extra: Record<string, unknown> = {}) => ({
  timestamp: ts,
  avg_cpu_used: cpu,
  avg_cpu_request: 1,
  avg_memory_used: memBytes,
  avg_memory_request: 2048 * MB,
  ...extra,
});

describe('buildTrendSeries', () => {
  it('plots CPU in cores and memory in MB, one label per row', () => {
    const s = buildTrendSeries(trend([row('2026-09-18T10:00:00Z', 0.5, 300 * MB), row('2026-09-18T11:00:00Z', 0.7, 320 * MB)]), 0.661, 329 * MB);
    expect(s.labels).toHaveLength(2);
    expect(s.cpu.usage).toEqual([0.5, 0.7]);
    expect(s.cpu.request).toEqual([1, 1]);
    expect(s.memory.usage).toEqual([300, 320]);
    expect(s.memory.request).toEqual([2048, 2048]);
  });

  it('draws the recommendation as a flat line the length of the series, converting memory from bytes', () => {
    const s = buildTrendSeries(trend([row('2026-09-18T10:00:00Z', 0.5, 300 * MB), row('2026-09-18T11:00:00Z', 0.7, 320 * MB)]), 0.661, 329 * MB);
    expect(s.cpu.recommended).toEqual([0.661, 0.661]);
    expect(s.memory.recommended).toEqual([329, 329]);
  });

  it('plots numeric strings as numbers wherever they arrive — the nb datasource and the JSONB both produce them', () => {
    const rows = [
      {
        timestamp: '2026-09-18T10:00:00Z',
        avg_cpu_used: '0.5',
        avg_cpu_request: '1',
        avg_cpu_limit: '2',
        avg_memory_used: String(300 * MB),
        max_pod_cpu_used: '0.9',
      },
    ];
    const s = buildTrendSeries(trend(rows, true), '0.661', String(329 * MB));
    expect(s.cpu.usage).toEqual([0.5]);
    expect(s.cpu.request).toEqual([1]);
    expect(s.cpu.limit).toEqual([2]);
    expect(s.cpu.busiest).toEqual([0.9]);
    expect(s.cpu.recommended).toEqual([0.661]);
    expect(s.memory.usage).toEqual([300]);
    expect(s.memory.recommended).toEqual([329]);
  });

  it('turns a value that does not read as a number into a gap, never a NaN', () => {
    const rows = [{ timestamp: '2026-09-18T10:00:00Z', avg_cpu_used: 'n/a', avg_memory_used: '' }];
    const s = buildTrendSeries(trend(rows), 'unknown', {});
    expect(s.cpu.usage).toEqual([null]);
    expect(s.memory.usage).toEqual([null]);
    expect(s.cpu.recommended).toBeNull();
    expect(s.memory.recommended).toBeNull();
  });

  it('tolerates a memory recommendation that arrives as a numeric string, and drops an absent one', () => {
    const rows = [row('2026-09-18T10:00:00Z', 0.5, 300 * MB)];
    expect(buildTrendSeries(trend(rows), 0.5, String(1024 * MB)).memory.recommended).toEqual([1024]);
    expect(buildTrendSeries(trend(rows), undefined, undefined).cpu.recommended).toBeNull();
    expect(buildTrendSeries(trend(rows), undefined, undefined).memory.recommended).toBeNull();
  });

  it('reports limits only when at least one row carries them', () => {
    const without = buildTrendSeries(trend([row('2026-09-18T10:00:00Z', 0.5, 300 * MB)]), 0.5, 0);
    expect(without.cpu.hasLimit).toBe(false);
    expect(without.cpu.limit).toEqual([null]);
    const withLimit = buildTrendSeries(
      trend([row('2026-09-18T10:00:00Z', 0.5, 300 * MB, { avg_cpu_limit: 2, avg_memory_limit: 4096 * MB })]),
      0.5,
      0
    );
    expect(withLimit.cpu.hasLimit).toBe(true);
    expect(withLimit.cpu.limit).toEqual([2]);
    expect(withLimit.memory.limit).toEqual([4096]);
  });

  it('carries the busiest pod only when the provider reported one', () => {
    const rows = [row('2026-09-18T10:00:00Z', 0.5, 300 * MB, { max_pod_cpu_used: 0.9, max_pod_memory_used: 500 * MB })];
    expect(buildTrendSeries(trend(rows, false), 0.5, 0).cpu.busiest).toBeNull();
    expect(buildTrendSeries(trend(rows, true), 0.5, 0).cpu.busiest).toEqual([0.9]);
    expect(buildTrendSeries(trend(rows, true), 0.5, 0).memory.busiest).toEqual([500]);
  });

  it('leaves a gap, not a zero, where a row has no value', () => {
    const s = buildTrendSeries(trend([{ timestamp: '2026-09-18T10:00:00Z' }]), 0.5, 0);
    expect(s.cpu.usage).toEqual([null]);
    expect(s.memory.usage).toEqual([null]);
  });

  it('is empty for an empty trend', () => {
    const s = buildTrendSeries({ rows: [], source: null, replicas: null, hasBusiestPod: false }, 0.5, 100 * MB);
    expect(s.labels).toEqual([]);
    expect(s.cpu.recommended).toEqual([]);
  });
});
