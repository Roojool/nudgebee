import { useState } from 'react';
import PropTypes from 'prop-types';
import { Box } from '@mui/material';
import { Label } from '@ui/Label';
import Tooltip from '@ui/Tooltip';
import { DropdownMenu } from '@ui/DropdownMenu';
import { ds } from '@utils/colors';

/**
 * Alert Status badge — same interaction shape as the Triage Status column's
 * NBStatusBadge: a Label with a dropdown arrow that opens a DropdownMenu of
 * the one valid transition, instead of a separate three-dot menu item.
 *
 * Open -> Close is always offered to anyone with write access. Closed -> Open
 * is only enabled when closedByName is set, i.e. only when the caller has
 * confirmed a human closed it via this same dropdown (event_get_status_change
 * only returns an entry for a manual close — see GetEventManualStatusChanges
 * on the backend) — most closes are automatic (ingestion, webhook resolves,
 * the inactive-resource sweep), and reopening those would just fight whatever
 * already correctly closed them. Every Closed badge still gets the dropdown
 * arrow for visual consistency across rows; when reopening isn't allowed, the
 * one menu item is present but disabled rather than the arrow disappearing.
 */
const AlertStatusBadge = ({ eventId, status, canToggle, closedByName, onToggle }) => {
  // Controlled, not default hover-dismiss: the mouse never leaves the badge
  // between hovering it and clicking it, so a plain uncontrolled Tooltip has
  // no mouseleave to hide on and stays open right through the click, covering
  // the DropdownMenu that opens under it. Closing explicitly on mousedown —
  // which fires before the click that opens the menu — is what NBStatusBadge
  // (Triage Status) already does for the same reason.
  const [tooltipOpen, setTooltipOpen] = useState(false);
  const isOpen = status === 'FIRING';
  const isClosed = status === 'CLOSED';
  const canReopen = isClosed && !!closedByName;
  const showArrow = canToggle && (isOpen || isClosed);

  const badgeLabel = (
    <Label
      margin='0'
      text={isOpen ? 'Open' : isClosed ? 'Closed' : status}
      variant={isOpen ? 'red' : isClosed ? 'grey' : ''}
      showDropdownArrow={showArrow}
    />
  );

  // Box (with onClick) must be the OUTERMOST element passed as `trigger` —
  // DropdownMenu clones the trigger it's given to attach its own open-menu
  // onClick (see DropdownMenu.tsx's enhancedTrigger), and that only reliably
  // reaches the DOM when the outermost element is a plain Box. Nesting Tooltip
  // outermost instead (Tooltip > Box) made the click depend on Tooltip
  // forwarding an onClick prop it was never designed to receive, which broke
  // reopening manually-closed events — mirrors NBStatusBadge's triggerBox,
  // which nests the same way for the same reason.
  const trigger = (
    <Box
      onClick={(e) => e.stopPropagation()}
      onMouseEnter={() => setTooltipOpen(true)}
      onMouseLeave={() => setTooltipOpen(false)}
      onMouseDown={() => setTooltipOpen(false)}
      sx={{ display: 'inline-flex', cursor: showArrow ? 'pointer' : 'default' }}
    >
      {isClosed ? (
        <Tooltip
          title={closedByName ? `Closed by ${closedByName}` : 'Closed'}
          open={tooltipOpen}
          disableHoverListener
          disableFocusListener
          disableTouchListener
        >
          <Box component='span' sx={{ display: 'inline-flex' }}>
            {badgeLabel}
          </Box>
        </Tooltip>
      ) : (
        badgeLabel
      )}
    </Box>
  );

  if (!showArrow) return trigger;

  // Dot colored to match the status being transitioned TO, same convention as
  // the Triage Status dropdown (NBStatusBadge's VARIANT_COLORS dot).
  const menuItems = [
    {
      id: 'toggle',
      label: (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 'var(--ds-space-2)' }}>
          <Box
            sx={{
              width: ds.space[2],
              height: ds.space[2],
              borderRadius: 'var(--ds-radius-pill)',
              backgroundColor: isOpen ? 'var(--ds-green-700)' : 'var(--ds-blue-700)',
              flexShrink: 0,
            }}
          />
          {isOpen ? 'Close' : 'Open'}
        </Box>
      ),
      disabled: isClosed && !canReopen,
      onSelect: () => onToggle(eventId, isOpen ? 'CLOSED' : 'FIRING'),
    },
  ];

  return <DropdownMenu trigger={trigger} items={menuItems} side='bottom' align='start' minWidth={120} />;
};

AlertStatusBadge.propTypes = {
  eventId: PropTypes.string.isRequired,
  status: PropTypes.string,
  canToggle: PropTypes.bool,
  closedByName: PropTypes.string,
  onToggle: PropTypes.func.isRequired,
};

export default AlertStatusBadge;
