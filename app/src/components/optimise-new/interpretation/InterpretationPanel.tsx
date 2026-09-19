/**
 * InterpretationPanel — the decision block at the top of a recommendation's
 * Details tab. One glance answers "what do I do, and can I": the verdict, how
 * confident the call is, how safe applying it is, and what it gives back. The
 * observed-usage strip beneath it is the statistic the call was sized on.
 *
 * Purely presentational — all slot text is assembled by `buildInterpretation`,
 * and the safety reading comes from the blast-radius pipeline via the caller.
 * Actions (Resolve / Create Ticket / …) deliberately live in the sticky
 * ActionBar, not here, so they aren't duplicated. The saving stays in the
 * drawer header directly above, which every tab shares.
 *
 * Built only from DS tokens (`@utils/colors`) + the DS `Label`, matching the
 * existing detail-panel callout (blue-100 surface, blue-200 border, 8px radius).
 */
import type { ReactNode } from 'react';
import { Box, Typography } from '@mui/material';
import TrendingUpIcon from '@mui/icons-material/TrendingUp';
import { ds } from 'src/utils/colors';
import { Label, type LabelTone } from '@ui/Label';
import { Link } from '@ui/Link';
import { Card } from '@ui/Card';
import Tooltip from '@ui/Tooltip';

export interface SafetyChip {
  label: string;
  tone: LabelTone;
  /** Why the reading is what it is, and what to do about it. */
  help: string;
}

export interface InterpretationData {
  /** ① One-line plain-language verdict — what to do. */
  verdict: string;
  /** Optional service/scope chip shown next to the verdict (e.g. "AWSLambda"). */
  serviceName?: string;
  /** Robustness chip shown next to the verdict (drives "is this a 3-day fluke?"). */
  confidence?: 'High' | 'Medium' | 'Low' | null;
  /** Safety reading from the blast-radius pipeline; absent when not assessed. */
  safety?: SafetyChip | null;
  /** Lookback the call was computed over, e.g. "14 days · 1.25-min step". */
  observedWindow?: string | null;
  /** ② Why it matters — the rationale. Rendered by the caller in its own
   *  disclosure lower down; carried here so the builder's contract is one object. */
  whyNow?: string;
  /** ③ Impact — dollar figure when present, else a reliability/security framing. */
  impact?: string;
  /** When true the impact is a real cost saving and is rendered in the savings tone. */
  impactIsCost?: boolean;
  /** ④ Risk / considerations — rule-family caveat (rollout, irreversibility, …). */
  risk?: string;
  /** Compact observed-usage strip rendered under the decision block (e.g. CPU P99 / rec). */
  evidence?: { label: string; value: string }[];
  /** When provided, renders a "View usage trend →" link in the Observed-usage
   *  slot that deep-links into the Evidence tab's time-series chart/table. */
  onViewEvidence?: () => void;
}

const CONFIDENCE_TONE: Record<'High' | 'Medium' | 'Low', LabelTone> = {
  High: 'success',
  Medium: 'warning',
  Low: 'neutral',
};

const Slot = ({ label, action, children }: { label?: string; action?: ReactNode; children: ReactNode }) => (
  <Box
    sx={{
      px: ds.space[4],
      py: ds.space[3],
      backgroundColor: 'rgba(255, 255, 255, 0.45)',
      '&:not(:last-of-type)': { borderBottom: `1px solid ${ds.gray[200]}` },
    }}
  >
    {(label || action) && (
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: ds.space[2], mb: ds.space[1] }}>
        <Typography
          sx={{
            fontFamily: 'var(--ds-font-display)',
            fontSize: ds.text.caption,
            fontWeight: ds.weight.medium,
            textTransform: 'uppercase',
            letterSpacing: '0.04em',
            color: ds.gray[500],
          }}
        >
          {label}
        </Typography>
        {action}
      </Box>
    )}
    {children}
  </Box>
);

const ViewTrendLink = ({ onClick }: { onClick: () => void }) => (
  // DS Link styled as the affordance. The Evidence tab is React-state controlled
  // (no route), so the hash href is a no-op we preventDefault on, then switch tabs.
  <Link
    href='#evidence'
    secondaryText
    onClick={(e) => {
      e.preventDefault();
      onClick();
    }}
    style={{ whiteSpace: 'nowrap' }}
  >
    <Box component='span' data-testid='view-usage-trend' sx={{ display: 'inline-flex', alignItems: 'center', gap: ds.space[0] }}>
      <TrendingUpIcon sx={{ fontSize: ds.text.bodyLg }} />
      View usage trend
    </Box>
  </Link>
);

const InterpretationPanel = ({
  verdict,
  serviceName,
  confidence,
  safety,
  observedWindow,
  impact,
  impactIsCost,
  risk,
  evidence,
  onViewEvidence,
}: InterpretationData) => {
  const hasEvidence = !!evidence && evidence.length > 0;
  const hasMeta = !!confidence || !!safety || !!observedWindow;
  // The block renders on anything it can say. A verdict is the usual lead, but
  // a recommendation with no resolvable title still has an impact line and a
  // safety reading, and losing those because the headline was empty would
  // make the panel less honest, not less cluttered.
  const hasDecision = !!verdict || hasMeta || !!impact;
  if (!hasDecision && !hasEvidence && !risk) return null;

  return (
    <Card variant='outlined' tone='neutral' elevation='flat' data-testid='interpretation-panel' sx={{ p: 0, overflow: 'hidden' }}>
      {/* The decision block: verdict, then the three readings that qualify it,
          then what it gives back. Formerly three labelled slots (Verdict,
          Impact, and a tooltip for the rationale) plus a safety chip two cards
          down; one block answers the question the reader opened the panel with. */}
      {hasDecision && (
        <Slot>
          <Box data-testid='decision-block' sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[1] }}>
            {(verdict || serviceName) && (
              <Box sx={{ display: 'flex', alignItems: 'center', gap: ds.space[2], flexWrap: 'wrap' }}>
                {verdict && (
                  <Typography sx={{ fontSize: ds.text.body, fontWeight: ds.weight.semibold, color: ds.gray[700], lineHeight: 1.45 }}>
                    {verdict}
                  </Typography>
                )}
                {serviceName && (
                  <Label size='sm' tone='neutral'>
                    {serviceName}
                  </Label>
                )}
              </Box>
            )}
            {hasMeta && (
              <Box sx={{ display: 'flex', alignItems: 'center', gap: ds.space[2], flexWrap: 'wrap' }}>
                {confidence && (
                  <Label size='sm' tone={CONFIDENCE_TONE[confidence]}>
                    {`${confidence} confidence`}
                  </Label>
                )}
                {safety && (
                  <Tooltip variant='explainer' title={`Safety: ${safety.label}`} desc={safety.help}>
                    <Box component='span' data-testid='decision-safety' sx={{ display: 'inline-flex', cursor: 'help' }}>
                      <Label size='sm' tone={safety.tone} dot>
                        {safety.label}
                      </Label>
                    </Box>
                  </Tooltip>
                )}
                {observedWindow && <Typography sx={{ fontSize: ds.text.caption, color: ds.gray[500] }}>{observedWindow}</Typography>}
              </Box>
            )}
            {impact && (
              <Typography
                data-testid='decision-impact'
                sx={{
                  fontSize: ds.text.small,
                  color: impactIsCost ? ds.green[600] : ds.gray[700],
                  fontWeight: impactIsCost ? ds.weight.semibold : ds.weight.regular,
                  lineHeight: 1.6,
                }}
              >
                {impact}
              </Typography>
            )}
          </Box>
        </Slot>
      )}

      {/* ② Observed-usage strip — the statistic the recommendation was sized on */}
      {hasEvidence && (
        <Slot label='Observed usage' action={onViewEvidence ? <ViewTrendLink onClick={onViewEvidence} /> : undefined}>
          <Box
            sx={{
              display: 'grid',
              // Equal-width columns so the metrics distribute evenly at any count
              // (1, 2, or 3) — space-between would strand 2 items at the far edges.
              gridTemplateColumns: `repeat(${evidence!.length}, minmax(0, 1fr))`,
              gap: ds.space[3],
              px: ds.space[3],
              py: ds.space[2],
              backgroundColor: ds.gray[100],
              border: `1px solid ${ds.gray[200]}`,
              borderRadius: ds.radius.sm,
            }}
          >
            {evidence!.map((e) => (
              <Box key={e.label} sx={{ display: 'flex', gap: ds.space[1], alignItems: 'baseline', minWidth: 0 }}>
                <Typography sx={{ fontSize: ds.text.caption, color: ds.gray[500], whiteSpace: 'nowrap' }}>{e.label}</Typography>
                <Typography sx={{ fontSize: ds.text.caption, fontFamily: ds.font.mono, color: ds.gray[700] }}>{e.value}</Typography>
              </Box>
            ))}
          </Box>
        </Slot>
      )}

      {/* ④ Risk & considerations — only when no Blast Radius card carries it */}
      {risk && (
        <Slot label='Risk & considerations'>
          <Typography sx={{ fontSize: ds.text.small, color: ds.amber[700], lineHeight: 1.6 }}>{risk}</Typography>
        </Slot>
      )}
    </Card>
  );
};

export default InterpretationPanel;
