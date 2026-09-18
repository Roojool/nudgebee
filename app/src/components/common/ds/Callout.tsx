/**
 * Callout — DS V2 (canonical / new).
 * Spec: app/design-system/primitives/feedback/callout.html
 *
 * Compact inline message that sits in the flow of a form, directly under the
 * field or control it explains. Distinct from `Banner` (page- or section-level
 * notice, its own padding and bottom margin) and `Toast` (transient).
 *
 * Variants per spec:
 *   tone = 'info' | 'success' | 'warning' | 'critical'
 *   composition = 'icon+message' | 'icon+message+action' (auto from `action`)
 *
 * Don't (per spec):
 *   - Don't use a Callout as a page- or section-level notice. That's a `Banner`.
 *   - Don't put more than one action in a Callout. It is a hint, not a decision.
 *   - Don't use it for validation errors a field renders itself (`Input` has
 *     `error`/`helperText`); a Callout explains, it doesn't mark a field invalid.
 *
 * Layout: no margins of its own, so the form controls spacing. It fills the
 * width it is given — put it in the flex/grid cell where the hint belongs.
 */
import * as React from 'react';
import { Box } from '@mui/material';
import InfoOutlinedIcon from '@mui/icons-material/InfoOutlined';
import CheckCircleOutlineIcon from '@mui/icons-material/CheckCircleOutline';
import ReportProblemOutlinedIcon from '@mui/icons-material/ReportProblemOutlined';
import ErrorOutlineIcon from '@mui/icons-material/ErrorOutline';
import { Button } from './Button';

export type CalloutTone = 'info' | 'success' | 'warning' | 'critical';

export interface CalloutAction {
  label: React.ReactNode;
  onClick: () => void;
  /** Rendered on the action button, for e2e selectors. */
  id?: string;
}

export interface CalloutProps extends Omit<React.HTMLAttributes<HTMLDivElement>, 'children'> {
  tone?: CalloutTone;
  /** The hint itself. `children` is accepted as an alias. */
  message?: React.ReactNode;
  children?: React.ReactNode;
  /** One optional link action, rendered under the message. */
  action?: CalloutAction;
  className?: string;
  id?: string;
}

const TONE_TOKENS: Record<CalloutTone, { bg: string; border: string; iconColor: string; textColor: string; Icon: React.ElementType }> = {
  info: {
    bg: 'var(--ds-blue-100)',
    border: 'var(--ds-blue-200)',
    iconColor: 'var(--ds-blue-600)',
    textColor: 'var(--ds-blue-700)',
    Icon: InfoOutlinedIcon,
  },
  success: {
    bg: 'var(--ds-green-100)',
    border: 'var(--ds-green-200)',
    iconColor: 'var(--ds-green-600)',
    textColor: 'var(--ds-green-700)',
    Icon: CheckCircleOutlineIcon,
  },
  warning: {
    bg: 'var(--ds-amber-100)',
    border: 'var(--ds-amber-200)',
    iconColor: 'var(--ds-amber-700)',
    textColor: 'var(--ds-amber-700)',
    Icon: ReportProblemOutlinedIcon,
  },
  critical: {
    bg: 'var(--ds-red-100)',
    border: 'var(--ds-red-200)',
    iconColor: 'var(--ds-red-600)',
    textColor: 'var(--ds-red-700)',
    Icon: ErrorOutlineIcon,
  },
};

export function Callout({ tone = 'info', message, children, action, className, id, ...rest }: CalloutProps): React.ReactElement {
  const cfg = TONE_TOKENS[tone];
  const Icon = cfg.Icon;
  const body = message ?? children;

  // A warning or a failure interrupts, a hint does not. aria-live follows the role so the
  // two never disagree (role='alert' already implies assertive).
  const role = tone === 'critical' || tone === 'warning' ? 'alert' : 'status';

  return (
    <Box
      // Spread first: the tone drives role/aria-live, so a caller can't replace
      // the accessibility semantics by passing its own.
      {...rest}
      id={id}
      className={className}
      role={role}
      aria-live={role === 'alert' ? 'assertive' : 'polite'}
      sx={{
        display: 'flex',
        alignItems: 'flex-start',
        gap: 'var(--ds-space-2)',
        padding: 'var(--ds-space-1) var(--ds-space-2)',
        backgroundColor: cfg.bg,
        border: `1px solid ${cfg.border}`,
        borderRadius: 'var(--ds-radius-md)',
        color: cfg.textColor,
        fontSize: 'var(--ds-text-caption)',
        lineHeight: 1.5,
      }}
    >
      <Box component={Icon as React.ElementType} aria-hidden='true' sx={{ fontSize: 14, color: cfg.iconColor, flexShrink: 0, marginTop: '1px' }} />
      <Box sx={{ flex: 1, minWidth: 0 }}>
        {body}
        {action && (
          <Box>
            <Button id={action.id} tone='link' size='xs' onClick={action.onClick}>
              {action.label}
            </Button>
          </Box>
        )}
      </Box>
    </Box>
  );
}

export default Callout;
