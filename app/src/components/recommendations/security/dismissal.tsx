/**
 * Dismiss / snooze / reactivate for the Security listings — the same action the
 * Cost and Configuration rows have had, so every finding on Optimise can be
 * parked or brought back.
 *
 * Everything on these tabs is a recommendation row (image-scan CVE, VM package
 * CVE, cloud posture resource, CIS target), so the Cost tab's modal and the
 * dismissal endpoint apply unchanged. A CIS rule row is a grouping, so its
 * action covers every finding behind it; the modal names the count.
 */
import { useCallback, useRef, useState } from 'react';
import DismissModal from '@components/optimise-new/DismissModal';
import recommendationApi from '@api1/recommendation';
import { toast as snackbar } from '@ui/Toast';

export interface DismissTarget {
  recs: any[];
  /** What the modal says it is dismissing when it is not one resource. */
  subject?: string;
}

export interface DismissMenuEntry {
  id: 'dismiss' | 'reactivate';
  label: string;
}

/**
 * The overflow-menu entry for a row, or null when the backend would refuse the
 * change: it accepts dismiss from Open and reactivate from Dismissed only, so
 * InProgress / Closed / Archive rows get no entry rather than an error toast.
 */
export const dismissMenuEntry = (status: string | undefined, canWrite: boolean): DismissMenuEntry | null => {
  if (!canWrite) return null;
  if (status === 'Dismissed') return { id: 'reactivate', label: 'Reactivate' };
  if (!status || status === 'Open') return { id: 'dismiss', label: 'Dismiss / snooze' };
  return null;
};

const BATCH = 10;

/** Reactivates every row; resolves with how many the backend confirmed. */
export async function reactivateRecommendations(recs: any[]): Promise<{ applied: number; failed: number }> {
  let applied = 0;
  let failed = 0;
  for (let i = 0; i < recs.length; i += BATCH) {
    const results = await Promise.allSettled(
      recs.slice(i, i + BATCH).map((rec) => recommendationApi.updateRecommendationDismissal(rec.account_id, rec.id, { dismissed: false }))
    );
    for (const r of results) {
      // Fail closed: an empty response means the change cannot be confirmed.
      if (r.status === 'fulfilled' && !r.value?.errors?.length && r.value?.data && r.value.data.applied !== false) applied++;
      else failed++;
    }
  }
  return { applied, failed };
}

/** A row, a plain list of rows, or a {recs, subject} target — always as a target. */
const toTarget = (recOrTarget: any): DismissTarget => {
  if (Array.isArray(recOrTarget)) return { recs: recOrTarget };
  if (Array.isArray(recOrTarget?.recs)) return recOrTarget;
  return { recs: recOrTarget ? [recOrTarget] : [] };
};

export function useRecommendationDismissal(onChanged: () => void) {
  const [target, setTarget] = useState<DismissTarget | null>(null);
  // Read through refs so the returned callbacks stay stable across renders
  // (callers put them in memo dependencies) and a reactivate in flight is not
  // started twice by a second click.
  const onChangedRef = useRef(onChanged);
  onChangedRef.current = onChanged;
  const reactivating = useRef(false);

  const requestDismiss = useCallback((recOrTarget: any) => {
    const t = toTarget(recOrTarget);
    if (t.recs.length === 0) {
      snackbar.info('Nothing to dismiss');
      return;
    }
    setTarget(t);
  }, []);

  const reactivate = useCallback(async (recOrTarget: any) => {
    const { recs } = toTarget(recOrTarget);
    if (recs.length === 0) {
      snackbar.info('Nothing to reactivate');
      return;
    }
    if (reactivating.current) return;
    reactivating.current = true;
    try {
      const { applied, failed } = await reactivateRecommendations(recs);
      if (applied === 0) {
        snackbar.error(recs.length === 1 ? 'Failed to reactivate recommendation' : 'Failed to reactivate the findings');
        return;
      }
      if (failed > 0) snackbar.warning(`Reactivated ${applied} of ${recs.length} findings`);
      else snackbar.success(recs.length === 1 ? 'Recommendation reactivated' : `Reactivated ${applied} findings`);
      onChangedRef.current();
    } catch {
      snackbar.error('Failed to reactivate recommendation');
    } finally {
      reactivating.current = false;
    }
  }, []);

  /** Dismiss an Open row, reactivate a Dismissed one. */
  const toggle = useCallback(
    (recOrTarget: any, status?: string) => {
      const first = toTarget(recOrTarget).recs[0];
      const current = status || first?.status;
      if (current === 'Dismissed') reactivate(recOrTarget);
      else requestDismiss(recOrTarget);
    },
    [reactivate, requestDismiss]
  );

  const modal = target ? (
    <DismissModal
      rec={target.recs[0]}
      recs={target.recs}
      subject={target.subject}
      onClose={() => setTarget(null)}
      onSuccess={() => {
        setTarget(null);
        onChangedRef.current();
      }}
    />
  ) : null;

  return { requestDismiss, reactivate, toggle, modal };
}
