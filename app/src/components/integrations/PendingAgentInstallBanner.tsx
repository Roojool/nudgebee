import { useCallback, useEffect, useMemo, useState } from 'react';
import { useSession } from 'next-auth/react';
import { Box } from '@mui/material';
import { Banner } from '@ui/Banner';
import apiKubernetes1 from '@api1/kubernetes1';
import {
  AGENT_CONNECTED,
  clearPendingAgentInstalls,
  PENDING_AGENT_INSTALL_EVENT,
  pendingAgentInstallKey,
  readPendingAgentInstalls,
  type PendingAgentInstall,
} from './pendingAgentInstall';

/**
 * Reminder for Kubernetes accounts created in the UI whose install command was
 * never run — the account exists, the agent table has no row for it, so nothing
 * is collecting data. Mounted once in the page layout: the "Add Kubernetes
 * Account" modal is reachable from the header on every page, so the reminder has
 * to follow the user rather than live on one screen.
 *
 * Deliberately not dismissible — dismissing it would lose the only signal that
 * the cluster is half-connected. It clears itself the moment the agent reports in.
 */
const POLL_MS = 30000;

const PendingAgentInstallBanner = () => {
  const [pending, setPending] = useState<PendingAgentInstall[]>([]);
  // The store is keyed by tenant, and the tenant is only known once the session
  // resolves — a read before that hits a different key and finds nothing. Re-read
  // when it lands, and again on a tenant switch.
  const { data: session } = useSession();
  const tenant = (session as { tenant?: { id?: string; name?: string } } | null)?.tenant;
  const tenantKey = tenant?.id || tenant?.name || '';

  const sync = useCallback(() => setPending(readPendingAgentInstalls()), []);

  useEffect(() => {
    sync();
    window.addEventListener(PENDING_AGENT_INSTALL_EVENT, sync);
    // Another tab finishing the install should clear this one too. Only our own
    // key is worth a re-read; `key` is null when a tab calls localStorage.clear(),
    // which does concern us.
    const handleStorage = (event: StorageEvent) => {
      if (event.key === null || event.key === pendingAgentInstallKey()) {
        sync();
      }
    };
    window.addEventListener('storage', handleStorage);
    return () => {
      window.removeEventListener(PENDING_AGENT_INSTALL_EVENT, sync);
      window.removeEventListener('storage', handleStorage);
    };
  }, [sync, tenantKey]);

  // Joined ids rather than the array itself: `sync` hands back a fresh array on
  // every event, and that identity change alone must not restart the poll.
  const pendingIds = useMemo(() => pending.map((entry) => entry.id), [pending]);
  const pendingIdsKey = pendingIds.join(',');

  useEffect(() => {
    if (!pendingIdsKey) {
      return undefined;
    }
    let cancelled = false;
    const check = () => {
      if (document.visibilityState !== 'visible') {
        return;
      }
      // No agent-type filter: this list mixes clusters (type k8s) with self-hosted
      // VM accounts (type proxy), and each account only carries agents of its own
      // kind, so filtering by id alone is exact.
      apiKubernetes1.listK8sAccAgentHealth(pendingIdsKey.split(','), null).then((res: any) => {
        if (cancelled) {
          return;
        }
        // CONNECTED, not the row's existence: an agent row is written when the
        // account is created, so every pending account has one from the start.
        const installed = (res?.data?.data?.agent || [])
          .filter((row: { status?: string }) => row?.status === AGENT_CONNECTED)
          .map((row: { cloud_account_id: string }) => row.cloud_account_id);
        // Writing fires PENDING_AGENT_INSTALL_EVENT, which re-syncs local state.
        clearPendingAgentInstalls(installed);
      });
    };
    check();
    const timer = setInterval(check, POLL_MS);
    // A hidden tab skips its ticks, so re-check on return rather than leaving a
    // stale banner up for the rest of the interval.
    const handleVisibility = () => {
      if (document.visibilityState === 'visible') {
        check();
      }
    };
    document.addEventListener('visibilitychange', handleVisibility);
    return () => {
      cancelled = true;
      clearInterval(timer);
      document.removeEventListener('visibilitychange', handleVisibility);
    };
  }, [pendingIdsKey]);

  if (pending.length === 0) {
    return null;
  }

  // One banner, not one per kind — the DS allows a single banner per surface, and
  // the next step differs by kind, so each kind gets its own line inside it.
  const clusters = pending.filter((entry) => entry.kind !== 'vm');
  const fleets = pending.filter((entry) => entry.kind === 'vm');

  const nameList = (entries: PendingAgentInstall[], fallbackNoun: string) => {
    const names = entries.map((entry) => entry.name).filter(Boolean);
    return names.length > 0 ? names.map((name) => `"${name}"`).join(', ') : `${entries.length} ${fallbackNoun}`;
  };

  return (
    <Box data-testid='pending-agent-install-banner' sx={{ mt: 'var(--ds-space-3)' }}>
      <Banner
        tone='warning'
        surface='page'
        title='Finish installing the agent'
        message={
          <>
            {clusters.length > 0 && (
              <Box component='div' data-testid='pending-agent-install-k8s'>
                {`No agent has connected for ${nameList(clusters, 'Kubernetes account(s)')} yet, so nothing is being collected from ${
                  clusters.length > 1 ? 'those clusters' : 'that cluster'
                }. Run the install command on your cluster.`}
              </Box>
            )}
            {fleets.length > 0 && (
              <Box component='div' data-testid='pending-agent-install-vm' sx={{ mt: clusters.length > 0 ? 'var(--ds-space-1)' : 0 }}>
                {`No agent has connected for ${nameList(fleets, 'self-hosted account(s)')} yet, so no machines are being discovered. Add a VM Agent
                  integration for each network segment — that is where the install command comes from.`.replace(/\s+/g, ' ')}
              </Box>
            )}
            <Box component='div' sx={{ mt: 'var(--ds-space-1)' }}>
              This message clears itself as soon as the agent connects.
            </Box>
          </>
        }
      />
    </Box>
  );
};

export default PendingAgentInstallBanner;
