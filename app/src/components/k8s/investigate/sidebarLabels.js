// Alert labels are the alerting source's own key/values. Nudgebee writes its
// bookkeeping into the same map (nb_*), and the sidebar used to print all of it
// on the first screen: a triage rule id of all zeros, which reads as a broken
// value, and — on PagerDuty events — the integration's routing key, which is a
// secret.
const INTERNAL_PREFIX = 'nb_';

export const isInternalLabel = (key) => String(key ?? '').startsWith(INTERNAL_PREFIX);

/** {alert labels}, {our own} — the second goes behind "Show internal fields". */
export const splitInternalLabels = (labels) => {
  const entries = Object.entries(labels || {});
  return [Object.fromEntries(entries.filter(([key]) => !isInternalLabel(key))), Object.fromEntries(entries.filter(([key]) => isInternalLabel(key)))];
};
