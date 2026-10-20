import { isInternalLabel, splitInternalLabels } from '@components/k8s/investigate/sidebarLabels';

describe('sidebar alert labels', () => {
  it('keeps the alerting source’s labels on the first screen', () => {
    const [shown] = splitInternalLabels({ alertname: 'PodMemoryReachingLimit', container: 'leaker', severity: 'warning' });
    expect(Object.keys(shown)).toEqual(['alertname', 'container', 'severity']);
  });

  it('moves our own bookkeeping out of the way', () => {
    // Verbatim from event 30dc2b38: an id of all zeros and a duplicate pointer.
    const [shown, internal] = splitInternalLabels({
      alertname: 'KubePodCrashLooping',
      nb_triage_rule_id: '00000000-0000-0000-0000-000000000000',
      nb_duplicate_of: 'aaf904ad-89be-4434-9c92-000000000000',
      nb_webhook_url: 'https://example.invalid/alert',
    });
    expect(Object.keys(shown)).toEqual(['alertname']);
    expect(Object.keys(internal)).toEqual(['nb_triage_rule_id', 'nb_duplicate_of', 'nb_webhook_url']);
  });

  it('handles an event with no labels at all', () => {
    expect(splitInternalLabels(undefined)).toEqual([{}, {}]);
  });

  it('treats only the nb_ prefix as internal', () => {
    expect(isInternalLabel('nb_status')).toBe(true);
    expect(isInternalLabel('namespace')).toBe(false);
    expect(isInternalLabel(undefined)).toBe(false);
  });
});
