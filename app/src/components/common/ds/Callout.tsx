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
import { Button } from './Button';
import { FEEDBACK_TONES, feedbackRole, resolveFeedbackTone, type FeedbackTone } from './internal/feedbackTones';

export type CalloutTone = FeedbackTone;

export interface CalloutAction {
  label: React.ReactNode;
  onClick: () => void;
  /** Rendered on the action button, for e2e selectors. */
  id?: string;
}

// role and aria-live are left out: the tone decides them (see below). Passing `role` is a
// compile error; `aria-live`, like any hyphenated JSX attribute, still type-checks, so it is
// also dropped at runtime.
export interface CalloutProps extends Omit<React.HTMLAttributes<HTMLDivElement>, 'children' | 'role' | 'aria-live'> {
  tone?: CalloutTone;
  /** The hint itself. `children` is accepted as an alias. */
  message?: React.ReactNode;
  children?: React.ReactNode;
  /** One optional link action, rendered under the message. */
  action?: CalloutAction;
}

export function Callout({ tone = 'info', message, children, action, ...rest }: CalloutProps): React.ReactElement {
  const safeTone = resolveFeedbackTone(tone, 'Callout');
  const cfg = FEEDBACK_TONES[safeTone];
  const Icon = cfg.Icon;
  const body = message ?? children;

  // Shared with Banner: warning/critical interrupt (role='alert'), info/success don't
  // (role='status'). The role alone carries the urgency — no explicit aria-live.
  const role = feedbackRole(safeTone);

  return (
    <Box
      // Spread first so the tone-driven role wins; aria-live={undefined} drops any aria-live a
      // caller passes (hyphenated attributes bypass TypeScript's excess-property check).
      {...rest}
      role={role}
      aria-live={undefined}
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
        lineHeight: 'var(--ds-text-caption-lh)',
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
