import { useEffect, useState } from 'react';
import apiWorkflow from '@api1/workflow';

// Long enough to skip mid-word keystrokes, short enough to feel attached to the edit.
const VALIDATE_DEBOUNCE_MS = 500;

interface FilterLiveValidationArgs {
  /** Off while the filter is built by the structured pickers, which only emit valid expressions. */
  enabled: boolean;
  filter: string;
  /** Account the check runs under. Absent = no check. */
  accountId?: string;
  triggerType: string;
  /** Trigger params as they would be saved, minus the filter. */
  params: Record<string, any>;
  /** A sample event that passes the trigger's account and phase gates, so the filter itself is evaluated. */
  samplePayload: Record<string, any> | null;
}

/**
 * Evaluates a hand-written filter with the server's own evaluator as the user types, and
 * returns the evaluator's error. A failed request returns no error: the check is advice, and
 * saving still goes through server validation.
 */
export function useFilterLiveValidation({ enabled, filter, accountId, triggerType, params, samplePayload }: FilterLiveValidationArgs) {
  // Keyed to the expression it was computed for, so an error never outlives the text it names.
  const [result, setResult] = useState<{ filter: string; error: string }>({ filter: '', error: '' });
  const [isChecking, setIsChecking] = useState(false);
  const trimmed = filter.trim();
  const paramsKey = JSON.stringify(params);
  const payloadKey = JSON.stringify(samplePayload);
  const canCheck = enabled && !!accountId && !!trimmed && !!samplePayload;

  useEffect(() => {
    if (!canCheck) {
      setResult({ filter: '', error: '' });
      setIsChecking(false);
      return;
    }
    let cancelled = false;
    setIsChecking(true);
    const timer = setTimeout(async () => {
      try {
        const response: any = await apiWorkflow.checkTriggerMatch({
          account_id: accountId as string,
          trigger_type: triggerType,
          params: { ...JSON.parse(paramsKey), filter: trimmed },
          payload: JSON.parse(payloadKey),
        });
        if (!cancelled) setResult({ filter: trimmed, error: response?.data?.workflow_check_trigger_match?.error || '' });
      } catch {
        if (!cancelled) setResult({ filter: trimmed, error: '' });
      } finally {
        if (!cancelled) setIsChecking(false);
      }
    }, VALIDATE_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [canCheck, accountId, triggerType, trimmed, paramsKey, payloadKey]);

  return { error: result.filter === trimmed ? result.error : '', isChecking };
}
