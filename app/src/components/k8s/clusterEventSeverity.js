// Severity buckets for the cluster summary Events/Errors card, in display order.
// Debug has no DS level, so it shares the Info badge and is shown only when non-zero.
const SEVERITY_BUCKETS = [
  { label: 'High', level: 'high', field: 'count_priority_high' },
  { label: 'Medium', level: 'medium', field: 'count_priority_medium' },
  { label: 'Low', level: 'low', field: 'count_priority_low' },
  { label: 'Info', level: 'info', field: 'count_priority_info' },
  { label: 'Debug', level: 'info', field: 'count_priority_debug', hideWhenZero: true },
];

export const buildSeverityData = (row) =>
  SEVERITY_BUCKETS.filter(({ field, hideWhenZero }) => !hideWhenZero || row?.[field]).map(({ label, level, field }) => ({
    label,
    level,
    value: row?.[field] || 0,
  }));
