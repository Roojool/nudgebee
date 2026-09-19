import { Box, Typography } from '@mui/material';
import { Modal } from '@ui/Modal';
import { Button } from '@ui/Button';
import { ds } from 'src/utils/colors';
import type { ApplyReadiness } from './applyReadiness';

interface SafetyAckModalProps {
  open: boolean;
  readiness: ApplyReadiness | null;
  /** The verb on the confirming button, e.g. "Deploy Fix". */
  confirmText: string;
  onCancel: () => void;
  onConfirm: () => void;
}

// The one human glance a Review/Risky verdict asks for before an apply runs:
// the same title and safeguard the readiness banner showed, restated at the
// moment of committing. Shared by every apply surface on Optimize so the gate
// reads the same whichever rule is being applied.
const SafetyAckModal = ({ open, readiness, confirmText, onCancel, onConfirm }: SafetyAckModalProps) => (
  <Modal width='sm' open={open} handleClose={onCancel} title='Ready to apply'>
    <Box sx={{ p: ds.space[4] }} id='resolve-modal-safety-confirm'>
      <Typography variant='body2' sx={{ color: ds.gray[700], fontWeight: ds.weight.semibold, mb: ds.space[2] }}>
        {readiness?.title}
      </Typography>
      <Typography variant='body2' sx={{ color: ds.gray[700], mb: ds.space[5] }}>
        {readiness?.message}
      </Typography>
      <Box sx={{ display: 'flex', justifyContent: 'flex-end', gap: ds.space[3] }}>
        <Button tone='secondary' size='md' onClick={onCancel} id='resolve-modal-safety-cancel'>
          Cancel
        </Button>
        <Button tone='primary' size='md' id='resolve-modal-safety-deploy' onClick={onConfirm}>
          {confirmText}
        </Button>
      </Box>
    </Box>
  </Modal>
);

export default SafetyAckModal;
