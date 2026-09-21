import { Box, Typography } from '@mui/material';
import { useRef, useState, useEffect } from 'react';
import { Chip } from '@ui/Chip';
import CopyButton from '@shared/buttons/CopyButton';
import { ds } from '@utils/colors';

interface ExecutedQueryInfoProps {
  /** The provider query that actually ran (evidence `additional_info.executed_query`). */
  query?: string | null;
  /** The log/metric provider it ran against, e.g. `loki` (evidence `additional_info.provider`). */
  provider?: string | null;
}

// Trace queries arrive as one very long line; collapsed to this many wrapped lines the
// evidence's own spans stay visible without scrolling, and the full query is one click away.
const COLLAPSED_LINES = 3;

/**
 * Shows the backend query an evidence was produced by, as a caption row above the
 * evidence's own data.
 *
 * The enrichers stamp `executed_query` + `provider` onto the evidence so a reader can
 * tell what was actually asked of the provider — a where-clause built by the agent is
 * rewritten (label mapping, account default filters) before it runs, so the recorded
 * query is not always the one the playbook asked for. Renders nothing when the backend
 * recorded no query (older evidence, providers that consume the where clause natively),
 * so callers can drop it in unconditionally.
 *
 * The query wraps and clamps to a few lines by default rather than running off the card
 * on one long line; a "Show more" toggle reveals the rest only when it actually overflows.
 */
const ExecutedQueryInfo = ({ query, provider }: ExecutedQueryInfoProps) => {
  const queryRef = useRef<HTMLElement | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [overflowing, setOverflowing] = useState(false);

  useEffect(() => {
    const el = queryRef.current;
    if (!el) return;
    // scrollHeight vs clientHeight is only meaningful while the box is clamped, so this
    // decides whether a "Show more" affordance is warranted at the current width.
    const check = () => setOverflowing(el.scrollHeight > el.clientHeight + 1);
    const ro = new ResizeObserver(check);
    ro.observe(el);
    check();
    return () => ro.disconnect();
  }, [query, expanded]);

  if (!query) return null;

  return (
    <Box data-testid='evidence-executed-query' sx={{ mb: ds.space[3] }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: ds.space[2], mb: ds.space[1] }}>
        <Typography sx={{ fontSize: ds.text.caption, fontWeight: ds.weight.medium, color: ds.gray[600], whiteSpace: 'nowrap' }}>
          Executed query
        </Typography>
        {provider && (
          <Chip variant='tag' size='xs' tone='subtle'>
            {provider}
          </Chip>
        )}
        <CopyButton text={query} size='xs' />
      </Box>
      <Box
        component='pre'
        ref={queryRef}
        sx={{
          margin: 0,
          fontFamily: ds.font.mono,
          fontSize: ds.text.small,
          lineHeight: 1.6,
          color: ds.gray[700],
          backgroundColor: ds.gray.alpha[100],
          border: `1px solid ${ds.gray[200]}`,
          borderRadius: ds.radius.sm,
          padding: `${ds.space[2]} ${ds.space[3]}`,
          whiteSpace: 'pre-wrap',
          wordBreak: 'break-word',
          ...(expanded
            ? {}
            : {
                display: '-webkit-box',
                WebkitLineClamp: COLLAPSED_LINES,
                WebkitBoxOrient: 'vertical',
                overflow: 'hidden',
              }),
        }}
      >
        {query}
      </Box>
      {(overflowing || expanded) && (
        <Typography
          component='button'
          type='button'
          data-testid='evidence-executed-query-toggle'
          onClick={() => setExpanded((v) => !v)}
          sx={{
            display: 'block',
            width: 'fit-content',
            mt: ds.space[1],
            padding: 0,
            border: 'none',
            background: 'none',
            fontFamily: 'inherit',
            fontSize: ds.text.caption,
            fontWeight: ds.weight.medium,
            color: ds.blue[600],
            cursor: 'pointer',
            '&:hover': { textDecoration: 'underline' },
          }}
        >
          {expanded ? 'Show less' : 'Show more'}
        </Typography>
      )}
    </Box>
  );
};

export default ExecutedQueryInfo;
