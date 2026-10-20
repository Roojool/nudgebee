/**
 * Shared tone palette and screen-reader semantics for the feedback primitives
 * (`Banner`, `Callout`). One table, so the two can't drift apart in colour or in
 * how urgently they announce themselves. Not for app code.
 */
import type * as React from 'react';
import InfoOutlinedIcon from '@mui/icons-material/InfoOutlined';
import CheckCircleOutlineIcon from '@mui/icons-material/CheckCircleOutline';
import ReportProblemOutlinedIcon from '@mui/icons-material/ReportProblemOutlined';
import ErrorOutlineIcon from '@mui/icons-material/ErrorOutline';

export type FeedbackTone = 'info' | 'success' | 'warning' | 'critical';

export interface FeedbackToneTokens {
  bg: string;
  border: string;
  iconColor: string;
  /** Message and title text. */
  textColor: string;
  Icon: React.ElementType;
}

export const FEEDBACK_TONES: Record<FeedbackTone, FeedbackToneTokens> = {
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

/**
 * Warning and critical interrupt a screen reader; info and success don't. The role
 * alone carries that (`alert` is implicitly assertive, `status` implicitly polite) —
 * don't add an explicit aria-live on top: it either contradicts the role or makes
 * some screen readers announce twice.
 */
export const feedbackRole = (tone: FeedbackTone): 'alert' | 'status' => (tone === 'critical' || tone === 'warning' ? 'alert' : 'status');

/**
 * Untyped (.jsx) callers can pass any string — e.g. MUI Alert's 'error'. Fall back to
 * 'info' instead of crashing on the palette lookup, and say so in development. An
 * own-property check, because `in` would also accept inherited keys like 'toString'.
 */
export const resolveFeedbackTone = (tone: unknown, component: string): FeedbackTone => {
  if (typeof tone === 'string' && Object.prototype.hasOwnProperty.call(FEEDBACK_TONES, tone)) {
    return tone as FeedbackTone;
  }
  if (process.env.NODE_ENV !== 'production') {
    // eslint-disable-next-line no-console
    console.warn(`${component}: unknown tone ${JSON.stringify(tone)} — falling back to 'info'. Use info | success | warning | critical.`);
  }
  return 'info';
};
