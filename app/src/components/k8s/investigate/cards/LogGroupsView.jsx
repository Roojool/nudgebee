import React, { useState } from 'react';
import { Box, Typography } from '@mui/material';
import ConsoleLogOutput from '@shared/viewers/ConsoleLogOutput';
import { Button } from '@ui/Button';
import { ds } from '@utils/colors';

// Up to this many lines the file itself is the clearest view, so it is shown as is.
// Past it, a list of what the lines say is readable where the raw file is not — the
// out-of-memory event in #38633 inserted 898 near-identical lines here.
export const RAW_LOG_LINE_LIMIT = 20;

const countColor = (level) => {
  if (level === 'error' || level === 'critical') {
    return ds.red[600];
  }
  if (level === 'warning') {
    return ds.amber[600];
  }
  return ds.gray[600];
};

// Time of day in UTC for a timestamp the line carried itself, or null when it does not
// parse. Epoch values arrive as numeric strings — seconds or milliseconds from log
// lines, nanoseconds from some log providers.
const timeOfDay = (value) => {
  if (!value) {
    return null;
  }
  let date;
  if (/^\d+(\.\d+)?$/.test(value)) {
    const epoch = Number(value);
    let millis = epoch;
    if (epoch >= 1e17) {
      millis = epoch / 1e6;
    } else if (epoch >= 1e14) {
      millis = epoch / 1e3;
    } else if (epoch < 1e12) {
      millis = epoch * 1000;
    }
    date = new Date(millis);
  } else {
    date = new Date(value);
  }
  return Number.isNaN(date.getTime()) ? null : date.toISOString().slice(11, 19);
};

const timeRange = (group) => {
  const first = timeOfDay(group.first_seen);
  const last = timeOfDay(group.last_seen) || first;
  if (first && last) {
    return first === last ? `${first} UTC` : `${first} – ${last} UTC`;
  }
  // An unfamiliar format still shows, as written, rather than disappearing.
  return first === last ? group.first_seen : `${group.first_seen} – ${group.last_seen}`;
};

const groupMeta = (group) => {
  const parts = [];
  if (group.level && group.level !== 'unknown') {
    parts.push(group.level);
  }
  if (group.first_seen) {
    parts.push(timeRange(group));
  }
  if (group.source) {
    parts.push(group.source);
  }
  return parts.join(' · ');
};

// Whether a log file is long enough, and grouped, to be shown as groups first.
export const shouldShowLogGroups = (additionalInfo) =>
  Array.isArray(additionalInfo?.log_groups) && additionalInfo.log_groups.length > 0 && (additionalInfo.log_total_lines ?? 0) > RAW_LOG_LINE_LIMIT;

// A group earns its own row when it could explain something: it is a warning or worse,
// or it repeats. Lines printed once at info level are routine, and listing each at the
// same weight as 898 errors buries the errors.
const isNotable = (group) => ['critical', 'error', 'warning'].includes(group.level) || group.count > 1;

const GroupRow = ({ group, testId }) => (
  <Box
    data-testid={testId}
    sx={{
      display: 'grid',
      gridTemplateColumns: `${ds.space.mul(4, 3)} 1fr`,
      gap: ds.space[3],
      alignItems: 'baseline',
      py: ds.space[2],
      borderTop: `1px solid ${ds.gray[200]}`,
    }}
  >
    <Typography
      sx={{
        fontFamily: ds.font.mono,
        fontWeight: ds.weight.semibold,
        fontSize: ds.text.small,
        lineHeight: 1.4,
        color: countColor(group.level),
        textAlign: 'right',
      }}
    >
      {group.count.toLocaleString()}
    </Typography>
    <Box sx={{ minWidth: 0 }}>
      <Typography sx={{ fontSize: ds.text.small, lineHeight: 1.4, color: ds.gray[700], overflowWrap: 'anywhere' }}>{group.text}</Typography>
      {groupMeta(group) && <Typography sx={{ fontSize: ds.text.caption, lineHeight: 1.4, color: ds.gray[500] }}>{groupMeta(group)}</Typography>}
    </Box>
  </Box>
);

// rawView replaces the plain-text viewer for cards whose rows have their own table.
const LogGroupsView = ({ logs, additionalInfo, idPrefix = 'log-groups', rawView }) => {
  const [showRaw, setShowRaw] = useState(false);
  const [showRoutine, setShowRoutine] = useState(false);

  const raw = rawView ?? <ConsoleLogOutput data={logs} />;

  if (!shouldShowLogGroups(additionalInfo)) {
    return raw;
  }

  const groups = additionalInfo.log_groups;
  const total = additionalInfo.log_total_lines;
  const notable = groups.filter(isNotable);
  const routine = groups.filter((group) => !isNotable(group));
  // With nothing notable, the routine lines are the whole story; hiding them would leave
  // an empty list.
  const routineVisible = showRoutine || notable.length === 0;
  const shown = routineVisible ? [...notable, ...routine] : notable;

  return (
    <Box data-testid={idPrefix} sx={{ pt: ds.space[3] }}>
      <Typography sx={{ fontSize: ds.text.caption, color: ds.gray[500], mb: ds.space[1] }}>
        {total.toLocaleString()} lines, grouped by what they say
      </Typography>
      {shown.map((group, index) => (
        <GroupRow key={`${index}-${group.text}`} group={group} testId={`${idPrefix}-row`} />
      ))}
      <Box
        sx={{ display: 'flex', gap: ds.space[2], alignItems: 'center', flexWrap: 'wrap', pt: ds.space[3], borderTop: `1px solid ${ds.gray[200]}` }}
      >
        {routine.length > 0 && notable.length > 0 && (
          <Button tone='ghost' size='sm' data-testid={`${idPrefix}-toggle-routine`} onClick={() => setShowRoutine(!showRoutine)}>
            {showRoutine ? 'Hide lines seen once' : `${routine.length} other line${routine.length > 1 ? 's' : ''}, each seen once`}
          </Button>
        )}
        <Button tone='secondary' size='sm' data-testid={`${idPrefix}-toggle-raw`} onClick={() => setShowRaw(!showRaw)}>
          {showRaw ? 'Hide raw lines' : `Show all ${total.toLocaleString()} lines`}
        </Button>
      </Box>
      {showRaw && <Box sx={{ mt: ds.space[3] }}>{raw}</Box>}
    </Box>
  );
};

export default LogGroupsView;
