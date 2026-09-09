/**
 * Turning a provider's metric series into chart-ready arrays.
 *
 * Every non-CloudWatch provider answers a metrics query with the same shape: a
 * list of series, each carrying its own `timestamps` / `values` pair and a
 * `metric` map of label key-values. Two screens render that — the Kubernetes
 * Query Metrics tab and, for a SaaS provider, the cloud account's Metrics tab —
 * so the alignment and downsampling live here rather than in either of them.
 *
 * The CloudWatch path is deliberately NOT a caller: it groups charts by
 * `metric.name` and series by `metric.resource_id`, fields only that source
 * emits, and it has its own parser.
 */

/** One series as the backend returns it. Extra keys are ignored. */
export interface MetricSeries {
  timestamps?: (number | string)[];
  values?: (number | string)[];
  metric?: Record<string, string>;
}

export interface SeriesChart {
  /** Sorted, de-duplicated, numeric timestamps across every series — the x axis. */
  labels: number[];
  dataset: { label: string; data: number[] }[];
  /** Series count BEFORE the dataset cap, for a truncation notice. */
  totalDatasets: number;
  /** Point count BEFORE decimation, for a truncation notice. */
  totalDataPoints: number;
  datasetsWereTruncated: boolean;
  pointsWereDecimated: boolean;
}

/** Chart.js slows to a crawl well before these; they cap what we hand it. */
export const MAX_CHART_DATASETS = 20;
export const MAX_CHART_DATA_POINTS = 500;

/**
 * Reduce `data` to at most `maxPoints`, keeping the maximum of each bucket so a
 * spike survives downsampling. Averaging here would flatten exactly the peaks
 * someone opened the chart to find.
 */
export const decimateData = (data: number[], maxPoints: number): number[] => {
  if (data.length <= maxPoints) {
    return data;
  }

  const step = data.length / maxPoints;
  const decimated: number[] = [];

  for (let i = 0; i < maxPoints; i++) {
    const startIdx = Math.floor(i * step);
    const endIdx = Math.floor((i + 1) * step);

    // Seeded with undefined rather than data[startIdx]: a non-finite first
    // sample would otherwise win every later comparison (everything compares
    // false against NaN) and blank the whole bucket. The backend sends null for
    // +Inf/-Inf/NaN, which parses to NaN here, so this is reachable on any
    // ratio query that divided by zero.
    let maxVal: number | undefined;
    for (let j = startIdx; j < endIdx && j < data.length; j++) {
      const candidate = data[j];
      if (!Number.isFinite(candidate)) {
        continue;
      }
      if (maxVal === undefined || candidate > maxVal) {
        maxVal = candidate;
      }
    }
    // A bucket with no finite sample keeps its first raw value, so a genuine
    // gap still reads as a gap rather than becoming a fabricated zero.
    decimated.push(maxVal === undefined ? data[startIdx] : maxVal);
  }

  return decimated;
};

/** Thin labels on the same bucket boundaries decimateData uses, so they stay aligned. */
export const decimateLabels = <T>(labels: T[], maxPoints: number): T[] => {
  if (labels.length <= maxPoints) {
    return labels;
  }
  const step = labels.length / maxPoints;
  return Array.from({ length: maxPoints }, (_, i) => labels[Math.floor(i * step)]);
};

/**
 * Render a series label from its metric labels, as `key=value` lines.
 * Series from different providers share no common identifying label, so the
 * whole map is shown rather than guessing which key names the series.
 */
export const seriesLabel = (metric: Record<string, string> | undefined): string =>
  Object.entries(metric || {})
    .map(([key, value]) => `${key}=${value}`)
    .join('\n');

/**
 * Align every series onto one shared time axis and downsample for the chart.
 *
 * Series do not have to agree on timestamps: the axis is their union, and a
 * series missing a point contributes 0 there. That is what the Query Metrics
 * chart has always done — a gap and a genuine zero look the same, which is why
 * the non-finite counts are reported separately by the caller rather than
 * inferred from the plotted values.
 */
export const buildSeriesChart = (payload: MetricSeries[] | undefined, options?: { maxDatasets?: number; maxDataPoints?: number }): SeriesChart => {
  const maxDatasets = options?.maxDatasets ?? MAX_CHART_DATASETS;
  const maxDataPoints = options?.maxDataPoints ?? MAX_CHART_DATA_POINTS;
  const series = payload ?? [];

  if (series.length === 0) {
    return { labels: [], dataset: [], totalDatasets: 0, totalDataPoints: 0, datasetsWereTruncated: false, pointsWereDecimated: false };
  }

  // Normalised to numbers and sorted numerically. Providers disagree on whether
  // a timestamp is a number or a numeric string, and a bare .sort() orders
  // lexicographically — which silently mis-orders the x axis the moment epochs
  // stop sharing a digit count, and makes a string key miss a numeric one in
  // the lookup below.
  const labels = [...new Set(series.flatMap((s) => (s?.timestamps ?? []).map((ts) => Number(ts))))].sort((a, b) => a - b);

  const limitedPayload = series.slice(0, maxDatasets);
  const datasetsWereTruncated = series.length > maxDatasets;

  const needsDecimation = labels.length > maxDataPoints;
  const decimatedLabels = needsDecimation ? decimateLabels(labels, maxDataPoints) : labels;

  const dataset = limitedPayload.map((item) => {
    // A Map keyed by timestamp, so aligning a series to the shared axis is O(n)
    // rather than an indexOf scan per point.
    const timestampToValue = new Map<number, number>();
    item?.timestamps?.forEach((ts, idx) => {
      timestampToValue.set(Number(ts), parseFloat(String(item?.values?.[idx])));
    });

    const values = labels.map((label) => {
      const value = timestampToValue.get(label);
      return value !== undefined ? value : 0;
    });

    return {
      label: seriesLabel(item?.metric),
      data: needsDecimation ? decimateData(values, maxDataPoints) : values,
    };
  });

  return {
    labels: decimatedLabels,
    dataset,
    totalDatasets: series.length,
    totalDataPoints: labels.length,
    datasetsWereTruncated,
    pointsWereDecimated: needsDecimation,
  };
};
