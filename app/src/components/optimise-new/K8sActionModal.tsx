import { useEffect, useMemo, useState } from 'react';
import { Box, Typography } from '@mui/material';
import { Modal } from '@ui/Modal';
import { Input } from '@ui/Input';
import { Banner } from '@ui/Banner';
import { toast as snackbar } from '@ui/Toast';
import CopyButton from '@shared/buttons/CopyButton';
import recommendationApi from '@api1/recommendation';
import { ds } from 'src/utils/colors';
import { applyReadiness } from './applyReadiness';
import SafetyAckModal from './SafetyAckModal';
import { buildK8sApplyData, k8sActionFor, parseSizeGi } from './k8sActions';

interface K8sActionModalProps {
  open: boolean;
  onClose: () => void;
  recommendation: any;
  /** False when the cluster's agent is not connected; undefined when unknown. */
  agentConnected?: boolean;
  onSuccess?: () => void;
}

// The confirm surface for the three agent fixes that are not pod right-sizing:
// reclaim a volume, resize a volume, scale down an abandoned workload. Same
// confirmation the cluster page asks for (type the name, or set the size), with
// the readiness verdict the cluster page does not show. Applying queues an
// agent task and writes the resolution row, exactly as the cluster page does.
const K8sActionModal = ({ open, onClose, recommendation: rec, agentConnected, onSuccess }: K8sActionModalProps) => {
  const action = useMemo(() => k8sActionFor(rec), [rec]);
  const readiness = useMemo(() => applyReadiness(rec, false, action?.safeguard), [rec, action]);
  const [typedName, setTypedName] = useState('');
  const [sizeText, setSizeText] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [safetyConfirm, setSafetyConfirm] = useState(false);

  useEffect(() => {
    if (open) {
      setTypedName('');
      setSizeText(action?.defaultSize != null ? String(action.defaultSize) : '');
      setSafetyConfirm(false);
    }
  }, [open, action]);

  if (!action) return null;

  const sizeGi = parseSizeGi(sizeText);
  const applyData = buildK8sApplyData(action, rec, { typedName, sizeGi });
  const agentDown = agentConnected === false;
  const canConfirm = !!applyData && !agentDown && !submitting;

  const submit = async ({ safetyAcked = false } = {}) => {
    if (!applyData) return;
    // Both typed-name rules are graded destructive by the backend, so their
    // verdict always asks for an acknowledgement: typing the resource's name is
    // it. Only the size input defers to the shared safety step.
    if (action.input === 'size' && readiness?.needsAck && !safetyAcked) {
      setSafetyConfirm(true);
      return;
    }
    setSubmitting(true);
    try {
      const res: any = await recommendationApi.applyRecommendation(rec.account_id || '', rec.id, applyData);
      if (res?.errors?.length) {
        snackbar.error(res.errors[0]?.message || `Failed to ${action.label.toLowerCase()}`);
        return;
      }
      snackbar.success(action.successMessage);
      onSuccess?.();
      onClose();
    } catch (err: any) {
      snackbar.error(err?.message || `Failed to ${action.label.toLowerCase()}`);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <>
      <Modal
        width='sm'
        open={open}
        handleClose={() => !submitting && onClose()}
        title={action.label}
        confirmText={action.confirmText}
        onConfirm={() => submit()}
        confirmDisabled={!canConfirm}
        loader={submitting}
        backdropClickClose={!submitting}
      >
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[3] }} id='k8s-action-modal'>
          {agentDown && (
            <Banner
              id='k8s-action-agent-banner'
              surface='section'
              tone='warning'
              title='Agent not connected'
              message='This cluster’s agent is not connected, so nothing can be applied from here until it reconnects. Copy Command still gives the kubectl steps.'
            />
          )}
          {readiness && (
            <Banner id='k8s-action-readiness' surface='section' tone={readiness.tone} title={readiness.title} message={readiness.message} />
          )}

          <Typography variant='body2' sx={{ color: ds.gray[700] }}>
            {action.summary}
          </Typography>

          <Box component='dl' sx={{ m: 0, display: 'grid', gridTemplateColumns: 'max-content 1fr', columnGap: ds.space[4], rowGap: ds.space[1] }}>
            {action.facts.map((fact) => (
              <Box key={fact.label} sx={{ display: 'contents' }}>
                <Typography component='dt' variant='body2' sx={{ color: ds.gray[600] }}>
                  {fact.label}
                </Typography>
                <Box component='dd' sx={{ m: 0, display: 'flex', alignItems: 'center', gap: ds.space[1], minWidth: 0 }}>
                  <Typography variant='body2' sx={{ color: ds.gray[700], fontWeight: ds.weight.medium, wordBreak: 'break-all' }}>
                    {fact.value}
                  </Typography>
                  {fact.copyable && <CopyButton text={fact.value} size='sm' />}
                </Box>
              </Box>
            ))}
          </Box>

          {action.input === 'typed-name' ? (
            <Input
              id='k8s-action-typed-name'
              size='sm'
              label={`Type ${action.subject} to confirm`}
              value={typedName}
              onChange={setTypedName}
              disabled={agentDown || submitting}
            />
          ) : (
            <Input
              id='k8s-action-size'
              size='sm'
              type='number'
              inputMode='decimal'
              label='New size'
              suffix='Gi'
              value={sizeText}
              onChange={setSizeText}
              error={sizeText && sizeGi == null ? 'Enter a size above zero' : undefined}
              disabled={agentDown || submitting}
            />
          )}
        </Box>
      </Modal>

      <SafetyAckModal
        open={safetyConfirm}
        readiness={readiness}
        confirmText={action.confirmText}
        onCancel={() => setSafetyConfirm(false)}
        onConfirm={() => {
          setSafetyConfirm(false);
          submit({ safetyAcked: true });
        }}
      />
    </>
  );
};

export default K8sActionModal;
