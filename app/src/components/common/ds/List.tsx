/**
 * List — DS V2 of legacy CustomListWithShowMore.
 * Spec: app/design-system/primitives/layout/list.html
 *
 * Vertical list of label rows with optional truncation ("show 5 / show all").
 * Distinct from `Table` — List has no columns; rows render whatever they want.
 *
 * Variants per spec:
 *   composition = 'label' | 'label+subtext' | 'icon+label' | 'icon+label+meta'
 *                 (consumer-driven via renderItem)
 *   truncate    = 'none' | { show: N, label?: string }
 *   divider     = 'none' | 'between'
 *   bordered    = boolean (the bordered/rounded surface; set false for a bare list)
 *
 * Built-in bullet composition:
 *   For the common "bulleted clickable text" case (the legacy
 *   CustomListWithShowMore shape) you can omit `renderItem` entirely and pass
 *   string items plus `bullet` / `lineClamp` / `onItemClick`. List then
 *   renders a leading brand bullet, per-item width-based line-clamp (fills the
 *   available width, then ellipsis) with a hover Tooltip carrying the full text
 *   only when the row actually overflows, and an optional click handler. Provide
 *   `renderItem` to take full control instead — the two are mutually exclusive.
 *
 * Don't (per spec):
 *   - Don't use List for > 50 items without virtualisation. That's a Table problem.
 */
import * as React from 'react';
import { Box, ButtonBase } from '@mui/material';
import Tooltip from '@ui/Tooltip';
import { ds } from '@utils/colors';

export type ListDivider = 'none' | 'between';

const TOOLTIP_MAX_HEIGHT = '400px';

export interface ListTruncate {
  /** Initial number of rows to show. Remainder collapses behind a "show N more" button. */
  show: number;
  /** Template for the toggle label. `{n}` is substituted with the hidden-row count. Default: `Show {n} more →`. */
  label?: string;
  /** Label when expanded. Default: `Show less ↑`. */
  collapseLabel?: string;
}

export interface ListProps<T> {
  items: T[];
  /**
   * Renders one row. Receives the item and its index. Omit to use the built-in
   * bullet composition (string items + `bullet`/`maxItemLength`/`onItemClick`).
   */
  renderItem?: (item: T, index: number) => React.ReactNode;
  /** Stable key extractor; defaults to index. */
  keyFor?: (item: T, index: number) => React.Key;
  truncate?: ListTruncate;
  divider?: ListDivider;
  /** Bordered/rounded surface around the rows. Default `true`. Set `false` for a bare list. */
  bordered?: boolean;
  /** Render when `items.length === 0`. */
  empty?: React.ReactNode;
  className?: string;
  id?: string;
  /** Group accessible name. */
  ariaLabel?: string;

  /* --- Built-in bullet composition (ignored when `renderItem` is provided) --- */
  /** Show a leading brand bullet dot on each row. */
  bullet?: boolean;
  /**
   * Clamp each row's text to this many lines, filling the available width and
   * showing a hover Tooltip with the full text only when it actually overflows.
   * Default 2.
   */
  lineClamp?: number;
  /** Click handler for the built-in row (makes the text interactive). */
  onItemClick?: (item: T, index: number) => void;
}

// Long lines are clipped by CSS (see clampSx below); offer the full text on hover.
// A simple length check decides whether a row can overflow — no per-row DOM
// measurement, which is fragile around web-font/layout load timing. The threshold
// is deliberately low: a shorter row never wraps even on a narrow panel, and a
// longer row that happens to fit on a very wide panel just repeats its own text on
// hover (harmless).
const TOOLTIP_LENGTH_THRESHOLD = 100;

function BulletRow<T>({
  item,
  index,
  bullet,
  lineClamp = 2,
  onItemClick,
}: {
  item: T;
  index: number;
  bullet?: boolean;
  lineClamp?: number;
  onItemClick?: (item: T, index: number) => void;
}) {
  const text = typeof item === 'string' ? item : String(item);
  const clickable = !!onItemClick;
  const mayTruncate = text.length > TOOLTIP_LENGTH_THRESHOLD;

  // 1 line → nowrap + horizontal ellipsis; >1 line → vertical -webkit-line-clamp.
  const clampSx =
    lineClamp === 1
      ? ({ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' } as const)
      : ({ display: '-webkit-box', WebkitLineClamp: lineClamp, WebkitBoxOrient: 'vertical', overflow: 'hidden', wordBreak: 'break-word' } as const);

  const textNode = (
    <Box
      onClick={clickable ? () => onItemClick!(item, index) : undefined}
      sx={{
        ...clampSx,
        flex: '1 1 auto',
        minWidth: 0,
        color: 'var(--ds-brand-500)',
        paddingLeft: 'var(--ds-space-1)',
        fontSize: 'var(--ds-text-body)',
        lineHeight: 1.5,
        cursor: clickable ? 'pointer' : 'default',
      }}
    >
      {text}
    </Box>
  );

  return (
    <Box sx={{ display: 'flex', alignItems: 'flex-start' }}>
      {bullet && (
        <Box
          sx={{
            width: '5px',
            height: '5px',
            bgcolor: 'var(--ds-brand-500)',
            borderRadius: '100%',
            marginTop: 'var(--ds-space-2)',
            marginRight: 'var(--ds-space-1)',
            flexShrink: 0,
            boxShadow: '0 0 0 2px rgba(59, 131, 246, 0.15)',
          }}
        />
      )}
      {mayTruncate ? (
        <Tooltip
          title={<Box sx={{ maxHeight: TOOLTIP_MAX_HEIGHT, overflow: 'auto', fontSize: 'var(--ds-text-small)', lineHeight: 1.5 }}>{text}</Box>}
          placement='top'
        >
          {textNode}
        </Tooltip>
      ) : (
        textNode
      )}
    </Box>
  );
}

export function List<T>({
  items,
  renderItem,
  keyFor,
  truncate,
  divider = 'between',
  bordered = true,
  empty,
  className,
  id,
  ariaLabel,
  bullet,
  lineClamp,
  onItemClick,
}: ListProps<T>) {
  const [expanded, setExpanded] = React.useState(false);

  if (items.length === 0) {
    if (empty === undefined) return null;
    return (
      <Box id={id} className={className} role='list' aria-label={ariaLabel}>
        {empty}
      </Box>
    );
  }

  const truncated = truncate && !expanded && items.length > truncate.show;
  const visibleItems = truncated ? items.slice(0, truncate!.show) : items;
  const hiddenCount = items.length - (truncate?.show ?? items.length);

  const labelTemplate = truncate?.label ?? 'Show {n} more →';
  const collapseLabel = truncate?.collapseLabel ?? 'Show less ↑';

  const renderRow = (item: T, i: number) =>
    renderItem ? renderItem(item, i) : <BulletRow item={item} index={i} bullet={bullet} lineClamp={lineClamp} onItemClick={onItemClick} />;

  return (
    <Box
      id={id}
      className={className}
      role='list'
      aria-label={ariaLabel}
      sx={{
        listStyle: 'none',
        padding: 0,
        margin: 0,
        ...(bordered && {
          border: '1px solid var(--ds-gray-200)',
          borderRadius: 'var(--ds-radius-md)',
          overflow: 'hidden',
        }),
      }}
    >
      {visibleItems.map((item, i) => (
        <Box
          key={keyFor ? keyFor(item, i) : i}
          role='listitem'
          sx={{
            // Bulleted text lists read as a tight cluster, not spaced-out cards, so the
            // bullet composition uses a compact row rhythm; renderItem rows keep the roomier default.
            padding: bullet ? 'var(--ds-space-1) var(--ds-space-2)' : 'var(--ds-space-3)',
            borderBottom: divider === 'between' && i < visibleItems.length - 1 ? '1px solid var(--ds-gray-200)' : 'none',
          }}
        >
          {renderRow(item, i)}
        </Box>
      ))}
      {truncate && items.length > truncate.show && (
        <Box
          sx={{
            padding: 'var(--ds-space-3)',
            textAlign: 'center',
            borderTop: divider === 'between' ? '1px solid var(--ds-gray-200)' : 'none',
          }}
        >
          <ButtonBase
            onClick={() => setExpanded((v) => !v)}
            sx={{
              fontSize: 'var(--ds-text-small)',
              fontWeight: 'var(--ds-font-weight-medium)',
              color: 'var(--ds-blue-600)',
              padding: `${ds.space[1]} ${ds.space[2]}`,
              borderRadius: 'var(--ds-radius-sm)',
              '&:hover': { color: 'var(--ds-blue-700)', backgroundColor: 'var(--ds-blue-100)' },
              '&.Mui-focusVisible': { outline: '2px solid var(--ds-blue-500)', outlineOffset: '1px' },
            }}
          >
            {expanded ? collapseLabel : labelTemplate.replace('{n}', String(hiddenCount))}
          </ButtonBase>
        </Box>
      )}
    </Box>
  );
}

export default List;
