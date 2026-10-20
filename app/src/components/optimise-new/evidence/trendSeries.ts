import type { PerPodTrend } from './perPodTrend';

/**
 * The per-resource series a right-sizing chart draws, built once from the
 * per-pod rows so the compact chart on the Details tab and the full one on the
 * Evidence tab plot the same numbers on the same units: CPU in cores, memory
 * in MB. The recommendation is a flat line the length of the series.
 */
export interface ResourceSeries {
  usage: (number | null)[];
  request: (number | null)[];
  limit: (number | null)[];
  hasLimit: boolean;
  /** The busiest pod at each point, when the provider reported one. */
  busiest: (number | null)[] | null;
  /** The recommended request, repeated per point so it plots as a line. */
  recommended: number[] | null;
}

export interface TrendSeries {
  labels: string[];
  cpu: ResourceSeries;
  memory: ResourceSeries;
}

const MB = 1024 * 1024;

// A value is a number when it reads as one: the nb datasource hands GraphQL
// rows through without parsing, and the recommendation JSONB is known to carry
// numeric strings in places, so a "0.5" must plot as 0.5, not as a gap. The
// old inline series passed such values straight to the chart, which coerced
// them; this keeps that tolerance while handing the chart real numbers.
// Anything that does not read as a finite number is a gap.
const num = (v: unknown): number | null => {
  if (v == null || v === '') return null;
  const n = Number(v);
  return Number.isFinite(n) ? n : null;
};

const toMb = (bytes: unknown): number | null => {
  const n = num(bytes);
  return n == null ? null : n / MB;
};

const flat = (value: number | null, length: number): number[] | null => (value == null ? null : new Array(length).fill(value));

export function buildTrendSeries(perPod: PerPodTrend, cpuRecommended: unknown, memoryRecommendedBytes: unknown): TrendSeries {
  const rows = perPod.rows;
  const cpuLimit = rows.map((r: any) => num(r.avg_cpu_limit));
  const memLimit = rows.map((r: any) => toMb(r.avg_memory_limit));
  return {
    labels: rows.map((r: any) =>
      new Date(r.timestamp).toLocaleDateString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
    ),
    cpu: {
      usage: rows.map((r: any) => num(r.avg_cpu_used)),
      request: rows.map((r: any) => num(r.avg_cpu_request)),
      limit: cpuLimit,
      hasLimit: cpuLimit.some((v) => v != null),
      busiest: perPod.hasBusiestPod ? rows.map((r: any) => num(r.max_pod_cpu_used)) : null,
      recommended: flat(num(cpuRecommended), rows.length),
    },
    memory: {
      usage: rows.map((r: any) => toMb(r.avg_memory_used)),
      request: rows.map((r: any) => toMb(r.avg_memory_request)),
      limit: memLimit,
      hasLimit: memLimit.some((v) => v != null),
      busiest: perPod.hasBusiestPod ? rows.map((r: any) => toMb(r.max_pod_memory_used)) : null,
      recommended: flat(toMb(memoryRecommendedBytes), rows.length),
    },
  };
}
