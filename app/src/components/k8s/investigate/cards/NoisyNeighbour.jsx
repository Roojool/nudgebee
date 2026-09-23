import { useMemo, useState } from 'react';
import { formatCores, formatMemory } from '@lib/formatter';
import { Box, Stack, Typography } from '@mui/material';
import PropTypes from 'prop-types';
import { ds } from '@utils/colors';
import Chip from '@ui/Chip';
import Tooltip from '@ui/Tooltip';
import ProgressBar from '@ui/ProgressBar';
import { buildMetricRows, DEFAULT_VISIBLE_ROWS, formatOffset, parseNoisyNeighbours, partitionRows, primaryMetricFor } from './noisyNeighbourData';

const formatGiB = (bytes) => (bytes > 0 ? `${formatMemory(bytes, 'bytes', 'gb', false)} GiB` : '-');

// ProgressBar reads `thresholds` as upper bounds: below `success` is green,
// below `warning` is amber, above it is red. Passing the band names as though
// they were floors paints a half-empty node amber, which is the same "the numbers
// disagree with the verdict" problem this card is being fixed for.
const METRICS = {
  memory: {
    label: 'Memory',
    format: formatGiB,
    // A node is short of memory well before it is full — the kernel starts
    // killing while there is still headroom on paper.
    thresholds: { success: 75, warning: 90 },
  },
  cpu: {
    label: 'CPU',
    format: (value) => (value > 0 ? formatCores(value) : '-'),
    thresholds: { success: 80, warning: 95 },
  },
};

// The same bands, said in words, for the collapsed secondary metric.
const verdictFor = (pct, thresholds) => {
  if (pct === null) {
    return 'not measured';
  }
  if (pct >= thresholds.warning) {
    return 'under pressure';
  }
  return pct >= thresholds.success ? 'busy' : 'no pressure';
};

const Text = ({ children, muted, bold }) => (
  <Typography
    sx={{
      color: muted ? ds.gray[600] : ds.gray[700],
      fontSize: 'var(--ds-text-small)',
      fontWeight: bold ? 'var(--ds-font-weight-semibold)' : 'var(--ds-font-weight-medium)',
      lineHeight: '16px',
    }}
  >
    {children}
  </Typography>
);
Text.propTypes = { children: PropTypes.node, muted: PropTypes.bool, bold: PropTypes.bool };

// Fixed widths for the numeric columns, so the workload name gets every pixel
// that is left. A five-column table gave "9 %" and "-" a quarter of the card
// each and squeezed the pod name into three wrapped lines — the rows came out
// ~200px tall. These are the whole layout.
const SHARE_W = 56;
const PCT_W = 40;
const VALUE_W = 72;

// One workload on one line: name and namespace truncate, the numbers stay put.
// Requests and limits are not columns any more — they were "-" on most rows, and
// the chip already says when one is missing. The tooltip carries the detail.
const WorkloadRow = ({ row, metric, accountId }) => {
  const spec = METRICS[metric];
  const flag = row.warnings[0];
  const detail = [
    `${row.namespace}/${row.podName}`,
    row.container ? `container ${row.container}` : null,
    row.request ? `request ${spec.format(row.request)}` : `no ${metric} request`,
    row.limit ? `limit ${spec.format(row.limit)}` : `no ${metric} limit`,
    ...row.warnings.slice(1).map((w) => w.detail),
  ]
    .filter(Boolean)
    .join(' · ');

  return (
    <Stack
      direction='row'
      alignItems='center'
      gap='var(--ds-space-2)'
      data-testid={`noisy-neighbour-row-${row.podName}`}
      sx={{ py: 'var(--ds-space-2)', borderTop: `1px solid ${ds.gray[200]}`, minWidth: 0 }}
    >
      <Tooltip title={detail}>
        <Box sx={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          <Box
            component='span'
            sx={{
              color: ds.gray[700],
              fontSize: 'var(--ds-text-small)',
              fontWeight: row.isSubject ? 'var(--ds-font-weight-semibold)' : 'var(--ds-font-weight-medium)',
            }}
          >
            {row.podName}
          </Box>
          <Box component='span' sx={{ color: ds.gray[600], fontSize: 'var(--ds-text-small)' }}>
            {` · ${row.namespace}`}
          </Box>
        </Box>
      </Tooltip>

      {row.isSubject ? (
        <Chip variant='status' tone='info' size='xs'>
          this pod
        </Chip>
      ) : null}
      {row.coKilledOffset !== null ? (
        <Tooltip
          title={`This pod was also OOM-killed on this node, ${formatOffset(row.coKilledOffset)} — the same machine ran out of memory.${
            row.eventId ? ' Open its incident.' : ''
          }`}
        >
          {/* Linked to that kill's own incident where we have it: naming the pod
              is the fix, opening it is what lets the reader check the claim. */}
          <Box
            component={row.eventId ? 'a' : 'span'}
            href={row.eventId ? `/investigate?id=${row.eventId}${accountId ? `&accountId=${accountId}` : ''}` : undefined}
            sx={{ textDecoration: 'none' }}
          >
            <Chip variant='status' tone='critical' size='xs'>
              {`OOM-killed ${formatOffset(row.coKilledOffset)}`}
            </Chip>
          </Box>
        </Tooltip>
      ) : null}
      {flag ? (
        <Tooltip title={flag.detail}>
          <span>
            <Chip variant='tag' hue='amber' size='xs'>
              {flag.label}
            </Chip>
          </span>
        </Tooltip>
      ) : null}

      {/* Share of the node, so the ranking is scannable without reading numbers. */}
      <Box sx={{ width: SHARE_W, flexShrink: 0 }}>
        {row.measured && row.nodePct !== null ? (
          <Box sx={{ height: 4, borderRadius: 'var(--ds-radius-pill)', backgroundColor: ds.gray[200], overflow: 'hidden' }}>
            <Box
              sx={{
                width: `${Math.min(row.nodePct, 100)}%`,
                height: '100%',
                borderRadius: 'var(--ds-radius-pill)',
                backgroundColor: row.isSubject ? 'var(--ds-brand-500)' : ds.gray[500],
              }}
            />
          </Box>
        ) : null}
      </Box>
      {/* "not sampled" is a finding, not a gap — a pod killed between two scrapes
          has no reading, and a 0% would say it was idle when it died. */}
      <Box sx={{ width: PCT_W, flexShrink: 0, textAlign: 'right' }}>
        <Text muted={!row.measured}>{row.measured && row.nodePct !== null ? `${row.nodePct.toFixed(0)}%` : '—'}</Text>
      </Box>
      <Box sx={{ width: VALUE_W, flexShrink: 0, textAlign: 'right' }}>
        <Text muted={!row.measured} bold={row.isSubject}>
          {row.measured ? spec.format(row.value) : 'not sampled'}
        </Text>
      </Box>
    </Stack>
  );
};
WorkloadRow.propTypes = { row: PropTypes.object, metric: PropTypes.string, accountId: PropTypes.string };

const MetricTable = ({ rows, metric, accountId }) => {
  const [expanded, setExpanded] = useState(false);
  const { visible, hidden } = useMemo(() => partitionRows(rows, DEFAULT_VISIBLE_ROWS), [rows]);
  const shown = expanded ? [...visible, ...hidden] : visible;

  return (
    <>
      <Stack direction='row' alignItems='center' gap='var(--ds-space-2)' sx={{ pb: 'var(--ds-space-1)' }}>
        <Box sx={{ flex: 1, minWidth: 0 }}>
          <Text muted>Workload</Text>
        </Box>
        <Box sx={{ width: SHARE_W + PCT_W + 8, flexShrink: 0, textAlign: 'right' }}>
          <Text muted>Node share</Text>
        </Box>
        <Box sx={{ width: VALUE_W, flexShrink: 0, textAlign: 'right' }}>
          <Text muted>Used</Text>
        </Box>
      </Stack>
      {shown.map((row) => (
        <WorkloadRow key={row.key} row={row} metric={metric} accountId={accountId} />
      ))}
      {hidden.length ? (
        <Box mt='var(--ds-space-2)' textAlign='center'>
          <Typography
            component='button'
            onClick={() => setExpanded((open) => !open)}
            sx={{
              background: 'none',
              border: 'none',
              color: 'var(--ds-brand-500)',
              cursor: 'pointer',
              fontSize: 'var(--ds-text-small)',
              fontWeight: 'var(--ds-font-weight-medium)',
              padding: 0,
            }}
          >
            {expanded ? 'Show fewer' : `Show all ${rows.length} workloads`}
          </Typography>
        </Box>
      ) : null}
    </>
  );
};
MetricTable.propTypes = { rows: PropTypes.array, metric: PropTypes.string, accountId: PropTypes.string };

// The metric that is not leading. It still has to state a verdict — "no
// pressure" is the answer the reader needs — but it costs one line, not a bar
// and fifteen rows, until they ask for it.
const SecondaryMetric = ({ rows, metric, used, allocatable, accountId }) => {
  const [open, setOpen] = useState(false);
  const spec = METRICS[metric];
  const pct = allocatable > 0 ? (used / allocatable) * 100 : null;
  const verdict = verdictFor(pct, spec.thresholds);

  return (
    <Box mt='var(--ds-space-4)'>
      <Stack
        component='button'
        direction='row'
        gap='var(--ds-space-2)'
        alignItems='center'
        onClick={() => setOpen((v) => !v)}
        sx={{ background: 'none', border: 'none', cursor: 'pointer', padding: 0, textAlign: 'left', width: '100%' }}
      >
        <Text bold>{spec.label}</Text>
        <Text muted>
          {pct === null ? 'not measured' : `${spec.format(used)} of ${spec.format(allocatable)} (${pct.toFixed(0)}%)`} — {verdict}
        </Text>
        <Typography sx={{ color: 'var(--ds-brand-500)', fontSize: 'var(--ds-text-small)' }}>{open ? '▴' : '▾'}</Typography>
      </Stack>
      {open ? (
        <Box mt='var(--ds-space-2)'>
          <MetricTable rows={rows} metric={metric} accountId={accountId} />
        </Box>
      ) : null}
    </Box>
  );
};
SecondaryMetric.propTypes = {
  rows: PropTypes.array,
  metric: PropTypes.string,
  used: PropTypes.number,
  allocatable: PropTypes.number,
  accountId: PropTypes.string,
};

const NoisyNeighbour = ({ row }) => {
  const card = useMemo(() => parseNoisyNeighbours(row), [row]);
  const primary = primaryMetricFor(row?.aggregation_key);

  const memoryRows = useMemo(
    () =>
      card ? buildMetricRows({ entries: card.neighbours, coKilled: card.coKilled, metric: 'memory', allocatable: card.memory.allocatable }) : [],
    [card]
  );
  const cpuRows = useMemo(
    () =>
      card && card.cpu
        ? buildMetricRows({ entries: card.cpuNeighbours, coKilled: card.coKilled, metric: 'cpu', allocatable: card.cpu.allocatable })
        : [],
    [card]
  );

  if (!card) {
    return null;
  }

  const sections = {
    memory: { rows: memoryRows, used: card.memory.used, allocatable: card.memory.allocatable },
    cpu: card.cpu ? { rows: cpuRows, used: card.cpu.used, allocatable: card.cpu.allocatable } : null,
  };
  const lead = sections[primary] ?? sections.memory;
  const leadMetric = sections[primary] ? primary : 'memory';
  const secondaryMetric = leadMetric === 'memory' ? 'cpu' : 'memory';
  const secondary = sections[secondaryMetric];
  const leadSpec = METRICS[leadMetric];

  if (!lead.rows.length && !secondary?.rows.length) {
    return null;
  }

  return (
    <Box mt='var(--ds-space-4)'>
      {lead.allocatable > 0 ? (
        <Box mb='var(--ds-space-3)'>
          <ProgressBar
            value={lead.used}
            max={lead.allocatable}
            label={leadSpec.label}
            thresholds={leadSpec.thresholds}
            formatValue={(value, max) => `${leadSpec.format(value)} of ${leadSpec.format(max)}`}
          />
        </Box>
      ) : null}
      <MetricTable rows={lead.rows} metric={leadMetric} accountId={row?.cloud_account_id} />
      {secondary && secondary.rows.length ? (
        <SecondaryMetric
          rows={secondary.rows}
          metric={secondaryMetric}
          used={secondary.used}
          allocatable={secondary.allocatable}
          accountId={row?.cloud_account_id}
        />
      ) : null}
    </Box>
  );
};
NoisyNeighbour.propTypes = {
  row: PropTypes.object,
};

export default NoisyNeighbour;
