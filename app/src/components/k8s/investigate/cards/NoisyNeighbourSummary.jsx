import { formatMemory } from '@lib/formatter';
import { Box, Stack, Typography } from '@mui/material';
import PropTypes from 'prop-types';
import { ds } from '@utils/colors';
import { parseNoisyNeighbours } from './noisyNeighbourData';

const formatGiB = (bytes) => `${formatMemory(bytes, 'bytes', 'gb', false)} GiB`;

const Stat = ({ label, value, emphasis }) => (
  <Box>
    <Typography
      sx={{
        color: 'var(--grey-80, var(--ds-gray-400))',
        display: 'block',
        fontSize: 'var(--ds-text-body)',
        fontWeight: 'var(--ds-font-weight-medium)',
        marginBottom: 'var(--ds-space-1)',
      }}
    >
      {label}
    </Typography>
    <Typography
      sx={{
        color: emphasis ? 'var(--ds-red-500)' : 'var(--Data-Points-main, var(--ds-brand-500))',
        display: 'block',
        fontSize: 'var(--ds-text-body-lg)',
        fontWeight: 'var(--ds-font-weight-medium)',
      }}
    >
      {value}
    </Typography>
  </Box>
);
Stat.propTypes = { label: PropTypes.string, value: PropTypes.node, emphasis: PropTypes.bool };

const NoisyNeighbourSummary = ({ row }) => {
  const card = parseNoisyNeighbours(row);
  if (!card) {
    return null;
  }

  const { allocatable, requested, used, limits } = card.memory;
  const committedPct = allocatable > 0 ? (requested / allocatable) * 100 : null;
  const usedPct = allocatable > 0 ? (used / allocatable) * 100 : null;
  const limitsPct = allocatable > 0 && limits > 0 ? (limits / allocatable) * 100 : null;

  return (
    <Box mt={ds.space.mul(0, 10)}>
      <Stack direction='row' justifyContent='space-between' flexWrap='wrap' gap='var(--ds-space-4)'>
        <Stack direction='row' gap='var(--ds-space-6)'>
          <Stat label='Node' value={card.nodeName ?? '-'} />
          <Stat label='Cluster' value={row?.cluster ?? '-'} />
          {/* The real count, from the node's own series. This used to be
              `total_pods`, which was the length of a list truncated at 15. */}
          {card.podsOnNode > 0 ? <Stat label='Pods on node' value={card.podsOnNode} /> : null}
        </Stack>

        <Stack direction='row' gap='var(--ds-space-6)'>
          {/* Committed leads, because it is the figure that actually says the
              machine has nowhere left to go: memory requests exceeded 95% of
              allocatable on 484 of 730 cards, while working set passed 85% on
              only 94. Reading "used" as the health of the node is what makes a
              full machine look fine. */}
          {committedPct !== null ? (
            <Stat
              label='Committed'
              value={`${formatGiB(requested)} of ${formatGiB(allocatable)} (${committedPct.toFixed(0)}%)`}
              emphasis={committedPct >= 95}
            />
          ) : null}
          {/* Over 100% the node is oversubscribed: its pods may collectively take
              more memory than it has, which is the condition that ends in a kill.
              Measured on the node from #39083 — requests 50%, limits 145%. */}
          {limitsPct !== null ? <Stat label='Limits' value={`${formatGiB(limits)} (${limitsPct.toFixed(0)}%)`} emphasis={limitsPct > 100} /> : null}
          {usedPct !== null ? <Stat label='In use' value={`${formatGiB(used)} (${usedPct.toFixed(0)}%)`} /> : null}
        </Stack>
      </Stack>
    </Box>
  );
};
NoisyNeighbourSummary.propTypes = {
  row: PropTypes.object,
};

export default NoisyNeighbourSummary;
