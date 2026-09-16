import React, { useEffect, useState } from 'react';
import PropTypes from 'prop-types';
import { Box } from '@mui/material';
import apiKubernetes from '@api1/kubernetes';
import { parseDateValue } from '@shared/format/Datetime';
import Text from '@shared/format/Text';
import SafeIcon from '@shared/icons/SafeIcon';
import Tooltip from '@ui/Tooltip';
import { Label } from '@ui/Label';
import { Link } from '@ui/Link';
import { ds } from '@utils/colors';
import { infoIcon } from '@assets';
import AlertStatusBadge from '@shared/widgets/AlertStatusBadge';
import { describeEventState, describeRecurredSince } from './eventState';

// Same grid as the sidebar's other rows, so the key lines up with its value.
const rowSx = { display: 'grid', alignItems: 'flex-start', gridTemplateColumns: `${ds.space.mul(1, 25)} 1fr` };
const valueSx = { display: 'flex', alignItems: 'center', gap: 'var(--ds-space-2)', minWidth: 0, minHeight: ds.space.mul(0, 9) };
const detailSx = { color: ds.gray[700], whiteSpace: 'nowrap' };

// Anything created within this long after the event is the event itself: created_at carries
// microseconds, the filter only milliseconds.
const SELF_CREATED_AT_SLACK_MS = 1000;

// The badge takes over for FIRING/CLOSED — the two statuses the Close/Open
// toggle actually knows how to act on — so "State" doubles as the Alert
// Status control instead of sitting next to a second, redundant row showing
// the same value. RESOLVED (and anything else) falls back to the old
// read-only Label: AlertStatusBadge has no dropdown transitions for it, and
// showing an arrow that does nothing would be worse than no arrow.
const StateLine = ({ state, testId, eventId, rawStatus, canToggle, closedByName, onToggle }) => (
  <Box sx={valueSx} data-testid={testId}>
    {rawStatus === 'FIRING' || rawStatus === 'CLOSED' ? (
      <AlertStatusBadge eventId={eventId} status={rawStatus} canToggle={canToggle} closedByName={closedByName} onToggle={onToggle} />
    ) : (
      <Label tone={state.tone} text={state.label} margin='0' />
    )}
    {state.detail && <Text value={state.detail} secondaryText sx={detailSx} />}
    {state.hint && (
      <Tooltip title={state.hint} arrow placement='bottom'>
        {/* SafeIcon can't hold a ref, and without one the tooltip never opens. */}
        <Box component='span' sx={{ display: 'inline-flex' }} data-testid='event-lifecycle-state-hint'>
          <SafeIcon src={infoIcon} alt='info' style={{ width: '13px', height: '13px', cursor: 'pointer', filter: 'opacity(0.5)' }} />
        </Box>
      </Tooltip>
    )}
  </Box>
);

StateLine.propTypes = {
  state: PropTypes.object.isRequired,
  testId: PropTypes.string,
  eventId: PropTypes.string,
  rawStatus: PropTypes.string,
  canToggle: PropTypes.bool,
  closedByName: PropTypes.string,
  onToggle: PropTypes.func,
};

/** "State" and "Recurred" rows: is this still going on, has it ended, did it come back. */
export const EventStateRows = ({ row, canToggleStatus, closedByName, onToggleStatus }) => {
  const [recurred, setRecurred] = useState(null);
  const accountId = row?.cloud_account_id;
  const isDemo = accountId === 'demo';
  const createdMs = row?.created_at ? parseDateValue(row.created_at).getTime() : NaN;
  const state = describeEventState({ status: row?.status, startsAt: row?.starts_at, endsAt: row?.ends_at });

  useEffect(() => {
    setRecurred(null);
    if (isDemo || !accountId || !row?.fingerprint || !Number.isFinite(createdMs)) return undefined;
    let cancelled = false;
    apiKubernetes
      .getK8sEventGroupings(
        1,
        0,
        { account_id: accountId, fingerprint: row.fingerprint, start_date: new Date(createdMs + SELF_CREATED_AT_SLACK_MS), end_date: new Date() },
        ['id', 'status', 'starts_at'],
        ['id', 'status', 'starts_at'],
        { name: 'starts_at', order: 'desc' }
      )
      .then((res) => {
        if (cancelled) return;
        setRecurred({
          count: Number(res?.data?.event_groupings_aggregate?.aggregate?.count) || 0,
          latest: res?.data?.event_groupings?.[0],
        });
      })
      // A failed lookup must not put a claim on screen, but it must not vanish either.
      .catch((err) => console.error('Failed to look up recurrences for fingerprint', row?.fingerprint, err));
    return () => {
      cancelled = true;
    };
  }, [accountId, isDemo, row?.fingerprint, createdMs]);

  const recurredSince = recurred && state ? describeRecurredSince({ ...recurred, ownState: state.state }) : null;

  return (
    <>
      {state && (
        <Box sx={rowSx}>
          <Text value={'State'} secondaryText />
          <StateLine
            state={state}
            testId='event-lifecycle-state'
            eventId={row?.id}
            rawStatus={row?.status}
            canToggle={canToggleStatus}
            closedByName={closedByName}
            onToggle={onToggleStatus}
          />
        </Box>
      )}
      {recurredSince && (
        <Box sx={rowSx}>
          <Text value={recurredSince.cameBack ? 'Came back' : 'Recurred'} secondaryText />
          <Box sx={valueSx} data-testid='event-recurred-since'>
            {recurredSince.latestId ? (
              <Link
                style={{ textDecoration: 'none', display: 'inline-flex', margin: '0' }}
                href={`/investigate?id=${recurredSince.latestId}&accountId=${accountId}`}
                openInNew={true}
              >
                <Label text={recurredSince.text} margin='0' tone='warning' />
              </Link>
            ) : (
              <Label text={recurredSince.text} margin='0' tone='warning' />
            )}
          </Box>
        </Box>
      )}
    </>
  );
};

EventStateRows.propTypes = {
  row: PropTypes.object,
  canToggleStatus: PropTypes.bool,
  closedByName: PropTypes.string,
  onToggleStatus: PropTypes.func,
};

/** The state of another event (e.g. the original of a duplicate), shown beside its link. */
export const LinkedEventState = ({ eventId, accountId }) => {
  const [linked, setLinked] = useState(null);

  useEffect(() => {
    setLinked(null);
    if (!eventId || accountId === 'demo') return undefined;
    let cancelled = false;
    apiKubernetes
      .getEventState(eventId)
      .then((ev) => {
        if (!cancelled && ev) setLinked(ev);
      })
      .catch((err) => console.error('Failed to get state for linked event', eventId, err));
    return () => {
      cancelled = true;
    };
  }, [eventId, accountId]);

  const state = linked ? describeEventState({ status: linked.status, startsAt: linked.starts_at, endsAt: linked.ends_at }) : null;
  if (!state) return null;
  const text = state.state === 'open' ? 'Open' : state.endedAgo ? `Ended ${state.endedAgo}` : 'Ended';
  return <Label tone={state.tone} text={text} margin='0' />;
};

LinkedEventState.propTypes = {
  eventId: PropTypes.string,
  accountId: PropTypes.string,
};
