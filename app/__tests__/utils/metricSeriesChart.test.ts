import { buildSeriesChart, decimateData } from '@utils/metricSeriesChart';

describe('decimateData', () => {
  it('returns the input untouched when it already fits', () => {
    expect(decimateData([1, 2, 3], 10)).toEqual([1, 2, 3]);
  });

  it('keeps the peak of each bucket rather than averaging it away', () => {
    // Two buckets of two: the maximum of each must survive.
    expect(decimateData([1, 9, 2, 7], 2)).toEqual([9, 7]);
  });

  it('does not let a leading NaN mask a real peak in the same bucket', () => {
    // The backend sends null for +Inf/-Inf/NaN, which parses to NaN here. Seeding
    // the running max with the first sample would leave the whole bucket NaN,
    // because every comparison against NaN is false.
    expect(decimateData([NaN, 9, 2, 7], 2)).toEqual([9, 7]);
  });

  it('leaves a bucket with no finite sample alone instead of inventing a zero', () => {
    const [first] = decimateData([NaN, NaN, 3, 4], 2);
    expect(Number.isNaN(first)).toBe(true);
  });
});

describe('buildSeriesChart', () => {
  it('returns an empty chart for no payload', () => {
    expect(buildSeriesChart(undefined).dataset).toEqual([]);
    expect(buildSeriesChart([]).labels).toEqual([]);
  });

  it('orders the axis numerically, not lexicographically', () => {
    // A bare .sort() would order these as 100, 5, 60 by string comparison.
    const chart = buildSeriesChart([{ timestamps: [100, 5, 60], values: [1, 2, 3], metric: { a: 'b' } }]);
    expect(chart.labels).toEqual([5, 60, 100]);
    // Values must follow their own timestamps through the reorder.
    expect(chart.dataset[0].data).toEqual([2, 3, 1]);
  });

  it('matches a string timestamp against a numeric one from another series', () => {
    // Providers disagree on the wire type; a string key would miss a numeric
    // lookup and silently plot 0.
    const chart = buildSeriesChart([
      { timestamps: [1, 2], values: [10, 20], metric: { s: 'num' } },
      { timestamps: ['1', '2'], values: [30, 40], metric: { s: 'str' } },
    ]);
    expect(chart.labels).toEqual([1, 2]);
    expect(chart.dataset[1].data).toEqual([30, 40]);
  });

  it('unions timestamps across series and fills a missing point with 0', () => {
    const chart = buildSeriesChart([
      { timestamps: [1, 3], values: [10, 30], metric: { s: 'a' } },
      { timestamps: [2], values: [20], metric: { s: 'b' } },
    ]);
    expect(chart.labels).toEqual([1, 2, 3]);
    expect(chart.dataset[0].data).toEqual([10, 0, 30]);
    expect(chart.dataset[1].data).toEqual([0, 20, 0]);
  });

  it('names a series from its whole label map', () => {
    const chart = buildSeriesChart([{ timestamps: [1], values: [1], metric: { pod: 'api', ns: 'prod' } }]);
    expect(chart.dataset[0].label).toBe('pod=api\nns=prod');
  });

  it('caps the dataset count and reports the original total', () => {
    const series = Array.from({ length: 5 }, (_, i) => ({ timestamps: [1], values: [i], metric: { i: String(i) } }));
    const chart = buildSeriesChart(series, { maxDatasets: 2 });
    expect(chart.dataset).toHaveLength(2);
    expect(chart.totalDatasets).toBe(5);
    expect(chart.datasetsWereTruncated).toBe(true);
  });

  it('decimates points and reports the original count', () => {
    const timestamps = Array.from({ length: 10 }, (_, i) => i);
    const chart = buildSeriesChart([{ timestamps, values: timestamps, metric: {} }], { maxDataPoints: 5 });
    expect(chart.labels).toHaveLength(5);
    expect(chart.dataset[0].data).toHaveLength(5);
    expect(chart.totalDataPoints).toBe(10);
    expect(chart.pointsWereDecimated).toBe(true);
  });
});
