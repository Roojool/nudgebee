/**
 * Switch — DS V2 of legacy CustomSwitch + standalone AntSwitch.
 * Spec:        app/design-system/primitives/forms/switch.html
 * Variants:    size = 'sm' | 'md'
 *              composition = 'switch-only' | 'label-left' | 'label-left+description' (auto from props)
 *              state = off | on | loading | disabled
 *
 *
 * Don't (per spec):
 *   - Use Switch in forms with a submit button — use Checkbox instead.
 *   - Put the label on the right of the switch.
 *   - Pair Switch with a confirmation Dialog for non-destructive changes.
 */
import * as React from 'react';
import MuiSwitch from '@mui/material/Switch';
import { styled } from '@mui/material/styles';
import CircularProgress from '@mui/material/CircularProgress';

type SwitchSize = 'sm' | 'md';

const SIZE_GEOMETRY: Record<SwitchSize, { w: number; h: number; thumb: number; translate: number }> = {
  sm: { w: 28, h: 16, thumb: 12, translate: 12 },
  md: { w: 36, h: 20, thumb: 16, translate: 16 },
};

export interface SwitchProps {
  /** Current on/off state */
  checked: boolean;
  /** Fires immediately on flip; no submit step */
  onChange: (event: React.ChangeEvent<HTMLInputElement>, checked: boolean) => void;
  /** Optional label rendered to the LEFT of the switch (per spec — never right) */
  label?: React.ReactNode;
  /** Optional secondary line below the label */
  description?: React.ReactNode;
  /** sm = 28×16, md (default) = 36×20 */
  size?: SwitchSize;
  /** Renders the switch as visually inactive and ignores input */
  disabled?: boolean;
  /** Replaces thumb with a spinner and disables interaction */
  loading?: boolean;
  id?: string;
  name?: string;
  'aria-label'?: string;
  /** data-* attributes (e.g. data-testid) land on the switch base — the span wrapping the input — as on MUI's Switch. */
  [dataAttribute: `data-${string}`]: string | number | boolean | undefined;
}

const StyledSwitch = styled(MuiSwitch, {
  shouldForwardProp: (prop) => prop !== 'dsSize',
})<{ dsSize: SwitchSize }>(({ dsSize }) => {
  const g = SIZE_GEOMETRY[dsSize];
  return {
    width: g.w,
    height: g.h,
    padding: 0,
    display: 'flex',
    '&:active': {
      '& .MuiSwitch-thumb': { width: g.thumb + 2 },
    },
    '& .MuiSwitch-switchBase': {
      padding: 2,
      color: 'var(--ds-background-100)',
      '&.Mui-checked': {
        transform: `translateX(${g.translate}px)`,
        color: 'var(--ds-background-100)',
        '& + .MuiSwitch-track': {
          opacity: 1,
          backgroundColor: 'var(--ds-blue-500)',
        },
      },
      '&.Mui-disabled + .MuiSwitch-track': {
        opacity: 0.5,
      },
    },
    '& .MuiSwitch-thumb': {
      boxShadow: '0 2px 4px 0 var(--ds-gray-alpha-200)',
      width: g.thumb,
      height: g.thumb,
      borderRadius: 'var(--ds-radius-pill)',
      transition: 'width 200ms',
    },
    '& .MuiSwitch-track': {
      borderRadius: 'var(--ds-radius-pill)',
      opacity: 1,
      backgroundColor: 'var(--ds-gray-300)',
      boxSizing: 'border-box',
    },
  };
});

// The row is a <label> that WRAPS the switch, so clicking the text (or description) flips it —
// as MUI's FormControlLabel does — without relying on ids. Callers sometimes reuse a static id
// across repeated rows; an htmlFor would then target the first such input on the page.
const noInactive = { shouldForwardProp: (prop: string): boolean => prop !== 'inactive' };

const Row = styled(
  'label',
  noInactive
)<{ inactive: boolean }>(({ inactive }) => ({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--ds-space-3)',
  cursor: inactive ? 'not-allowed' : 'pointer',
}));

const RowSpread = styled(
  'label',
  noInactive
)<{ inactive: boolean }>(({ inactive }) => ({
  display: 'flex',
  alignItems: 'flex-start',
  justifyContent: 'space-between',
  gap: 'var(--ds-space-3)',
  cursor: inactive ? 'not-allowed' : 'pointer',
}));

const LabelText = styled(
  'span',
  noInactive
)<{ inactive: boolean }>(({ inactive }) => ({
  display: 'block',
  fontSize: 'var(--ds-text-body)',
  fontWeight: 'var(--ds-font-weight-medium)',
  color: inactive ? 'var(--ds-gray-500)' : 'var(--ds-gray-700)',
}));

const DescriptionText = styled('span')({
  display: 'block',
  fontSize: 'var(--ds-text-small)',
  color: 'var(--ds-gray-600)',
  marginTop: 2,
});

const SpinnerSlot = styled('span', {
  shouldForwardProp: (prop) => prop !== 'dsSize',
})<{ dsSize: SwitchSize }>(({ dsSize }) => ({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: SIZE_GEOMETRY[dsSize].w,
  height: SIZE_GEOMETRY[dsSize].h,
}));

export function Switch({
  checked,
  onChange,
  label,
  description,
  size = 'md',
  disabled = false,
  loading = false,
  id,
  name,
  'aria-label': ariaLabel,
  ...rest
}: SwitchProps) {
  const isInteractionDisabled = disabled || loading;
  const generatedId = React.useId();
  const inputId = id ?? generatedId;
  // Unique even when a caller reuses a static `id`.
  const labelId = `${generatedId}-label`;
  // Only data-* is part of the API; untyped callers' other props (className, onClick, …) are dropped.
  const dataAttributes = Object.fromEntries(Object.entries(rest).filter(([key]) => key.startsWith('data-')));
  // A string label names the input directly; any other label names it by reference, so a
  // description inside the wrapping <label> doesn't become part of the name.
  const labelledBy = ariaLabel === undefined && label !== undefined && typeof label !== 'string' ? labelId : undefined;

  const switchEl = loading ? (
    <SpinnerSlot dsSize={size} aria-busy='true' {...dataAttributes}>
      <CircularProgress size={size === 'sm' ? 12 : 14} sx={{ color: 'var(--ds-blue-500)' }} />
    </SpinnerSlot>
  ) : (
    <StyledSwitch
      {...dataAttributes}
      dsSize={size}
      checked={checked}
      onChange={onChange}
      disabled={isInteractionDisabled}
      id={inputId}
      name={name}
      inputProps={{
        'aria-label': ariaLabel ?? (typeof label === 'string' ? label : undefined),
        'aria-labelledby': labelledBy,
      }}
    />
  );

  if (description !== undefined) {
    return (
      <RowSpread inactive={isInteractionDisabled}>
        <span>
          {label !== undefined && (
            <LabelText id={labelId} inactive={isInteractionDisabled}>
              {label}
            </LabelText>
          )}
          <DescriptionText>{description}</DescriptionText>
        </span>
        {switchEl}
      </RowSpread>
    );
  }

  if (label !== undefined) {
    return (
      <Row inactive={isInteractionDisabled}>
        <LabelText id={labelId} inactive={isInteractionDisabled}>
          {label}
        </LabelText>
        {switchEl}
      </Row>
    );
  }

  return switchEl;
}

export default Switch;
