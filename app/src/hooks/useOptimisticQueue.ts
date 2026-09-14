import { useCallback, useEffect, useRef, useState } from 'react';
import { QueuedMessage } from './useQueuedMessages';

// Pending rows are presentation only: never retry or dispatch a request here.
export function useOptimisticQueue(scope: string, capacity: number, serverCount: number) {
  const [entries, setEntries] = useState<QueuedMessage[]>([]);
  const pending = useRef(new Map<string, QueuedMessage>());
  const scopeRef = useRef(scope);
  scopeRef.current = scope;
  useEffect(() => {
    pending.current.clear();
    setEntries([]);
  }, [scope]);
  const submit = useCallback(
    (text: string, images: QueuedMessage['images'], persist: () => Promise<unknown>, isForeground = false) => {
      if (!isForeground && serverCount + [...pending.current.values()].filter((entry) => entry.saving && !entry.isForeground).length >= capacity)
        return false;
      const entry: QueuedMessage = { id: crypto.randomUUID(), text, images, createdAt: Date.now(), user: 'You', saving: true, isForeground };
      pending.current.set(entry.id, entry);
      setEntries((prev) => [...prev, entry]);
      void (async () => {
        try {
          // Foreground investigation handlers report failure as false after showing
          // their error. That is not an acknowledgement, nor proof of non-admission.
          if ((await persist()) === false) throw new Error('Submission was not confirmed.');
          if (scopeRef.current !== scope || pending.current.get(entry.id) !== entry) return;
          pending.current.delete(entry.id);
          setEntries((prev) => prev.filter((item) => item.id !== entry.id));
        } catch (error) {
          if (scopeRef.current !== scope || pending.current.get(entry.id) !== entry) return;
          const notAdmitted = Boolean((error as { notAdmitted?: boolean })?.notAdmitted);
          const failed = {
            ...entry,
            saving: false,
            notAdmitted,
            saveError: notAdmitted ? (error as Error).message : 'Couldn’t confirm saving—refresh to check.',
          };
          pending.current.set(entry.id, failed);
          setEntries((prev) => prev.map((item) => (item.id === entry.id ? failed : item)));
        }
      })();
      return true;
    },
    [scope, capacity, serverCount]
  );
  const takeDraft = useCallback((id: string) => {
    const entry = pending.current.get(id);
    if (!entry?.notAdmitted) return null;
    pending.current.delete(id);
    setEntries((prev) => prev.filter((item) => item.id !== id));
    return entry;
  }, []);
  return { entries, submit, takeDraft };
}
