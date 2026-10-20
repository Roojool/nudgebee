import { buildSeverityData } from '../clusterEventSeverity';

// Shape of the k8s_event_groupings row captured on test for #38493.
const ROW = {
  event_count: 3178,
  count_priority_high: 1328,
  count_priority_medium: 1540,
  count_priority_low: 0,
  count_priority_debug: 0,
  count_priority_info: 310,
};

describe('buildSeverityData', () => {
  it('shows an Info count so the severity counts add up to the total', () => {
    const data = buildSeverityData(ROW);
    expect(data.find((d) => d.label === 'Info')).toEqual({ label: 'Info', level: 'info', value: 310 });
    expect(data.reduce((sum, d) => sum + d.value, 0)).toBe(ROW.event_count);
  });

  it('hides Debug when it is zero, since it shares the Info badge', () => {
    expect(buildSeverityData(ROW).map((d) => d.label)).toEqual(['High', 'Medium', 'Low', 'Info']);
  });

  it('keeps Debug when it has events', () => {
    const data = buildSeverityData({ ...ROW, count_priority_debug: 4 });
    expect(data.map((d) => d.label)).toEqual(['High', 'Medium', 'Low', 'Info', 'Debug']);
  });

  it('defaults every count to 0 when the row is missing', () => {
    expect(buildSeverityData(undefined)).toEqual([
      { label: 'High', level: 'high', value: 0 },
      { label: 'Medium', level: 'medium', value: 0 },
      { label: 'Low', level: 'low', value: 0 },
      { label: 'Info', level: 'info', value: 0 },
    ]);
  });
});
