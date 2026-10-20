import React, { useState } from 'react';
import Box from '@mui/material/Box';
import { ds } from 'src/utils/colors';
import { Button } from '@ui/Button';
import { Banner } from '@ui/Banner';
import { DiffViewer } from '@ui/DiffViewer';
import { Modal } from '@ui/Modal';
import { toast as snackbar } from '@ui/Toast';
import k8sApi, { firstGraphQLError, type EventAlertControls } from '@api1/kubernetes1';
import {
  alertRuleApplyLabel,
  alertRuleChangeSummary,
  alertRuleChangeWarning,
  alertRuleMuteConfirm,
  isAlertRuleChangeApplied,
  type AlertRuleChange,
} from './remediationAlertRule';

// Event-level listeners (the "Stop this alert" row) reload when a plan action
// changes the rule.
export const ALERT_RULE_CHANGED_EVENT = 'nb:alert-rule-changed';

// A mute from the plan uses the screen's default duration.
const PLAN_MUTE_HOURS = 168;

type Props = {
  change: AlertRuleChange;
  controls: EventAlertControls | null;
  accountId: string;
  eventId: string;
  canRun: boolean;
  onApplied: () => void;
};

// The body of a remediation action that changes the alert rule itself: what
// changes, the caution the backend attached, and one button that applies it
// through the same path as Alert Manager — not a shell command.
export default function AlertRuleChangeAction({ change, controls, accountId, eventId, canRun, onApplied }: Props) {
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const applied = isAlertRuleChangeApplied(change, controls);
  const warning = alertRuleChangeWarning(change, controls);
  const label = alertRuleApplyLabel(change);
  const summary = alertRuleChangeSummary(change, controls);

  const apply = async () => {
    setBusy(true);
    let res: any;
    if (change.operation === 'edit') {
      res = await k8sApi.applyRuleChange({
        rule_id: change.rule_id || '',
        account_id: accountId,
        expr: change.expr || '',
        duration: change.duration || '',
        expected_expr: change.expected_expr || '',
        expected_duration: change.expected_duration || '',
      });
    } else if (change.operation === 'disable') {
      res = await k8sApi.disableAlertManager({
        accountId,
        alert: controls?.rule?.alert || controls?.alert || '',
        enable: false,
        id: change.rule_id,
        namespace: '',
        group: '',
      });
    } else {
      res = await k8sApi.updateEventMute({
        event_id: eventId,
        account_id: accountId,
        scope: change.mute_scope || 'rule',
        duration_hours: PLAN_MUTE_HOURS,
        muted: true,
      });
    }
    setBusy(false);
    if (res instanceof Error || res?.data?.errors?.length > 0 || res?.data?.data?.errors) {
      const reason = firstGraphQLError(res);
      snackbar.error(`Could not apply the rule change${reason ? `: ${reason}` : ''}`);
      return;
    }
    snackbar.success(change.operation === 'edit' ? 'Alert rule updated' : change.operation === 'disable' ? 'Alert rule turned off' : 'Alert muted');
    setConfirmOpen(false);
    onApplied();
    window.dispatchEvent(new Event(ALERT_RULE_CHANGED_EVENT));
  };

  const muteConfirm = change.operation === 'mute' ? alertRuleMuteConfirm(change, controls, PLAN_MUTE_HOURS / 24) : null;
  const confirmText =
    change.operation === 'edit'
      ? 'The rule will be changed where it is defined, as shown.'
      : change.operation === 'disable'
      ? 'The alert stops firing for everyone until it is turned back on.'
      : muteConfirm?.text || '';

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[3] }}>
      {change.operation === 'edit' && change.expr ? (
        <DiffViewer
          originalCode={change.expected_expr || ''}
          newCode={change.expr}
          language='text'
          title='Rule expression'
          leftLabel='Now'
          rightLabel='Proposed'
          collapsible={false}
          maxHeight={260}
        />
      ) : null}
      {change.operation === 'edit' && change.duration ? (
        <Box sx={{ fontSize: ds.text.small, color: ds.gray[700] }}>{`Fires after: ${change.expected_duration || 'immediately'} → ${
          change.duration
        }`}</Box>
      ) : null}
      {summary ? <Box sx={{ fontSize: ds.text.small, color: ds.gray[700] }}>{summary}</Box> : null}
      {warning && !applied ? <Banner tone='warning' surface='section' message={warning} /> : null}
      {!applied ? (
        <Box>
          <Button
            tone='primary'
            size='xs'
            onClick={() => setConfirmOpen(true)}
            disabled={!canRun || busy}
            data-testid='remediation-apply-rule-change'
          >
            {label}
          </Button>
        </Box>
      ) : null}
      <Modal
        open={confirmOpen}
        handleClose={() => setConfirmOpen(false)}
        title={muteConfirm?.title || `${label} — ${controls?.alert || 'alert rule'}`}
        confirmText={label}
        onConfirm={apply}
        loader={busy}
        backdropClickClose={!busy}
      >
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[3] }}>
          <Box sx={{ fontSize: ds.text.body }}>{confirmText}</Box>
          {warning ? <Banner tone='warning' surface='section' message={warning} /> : null}
        </Box>
      </Modal>
    </Box>
  );
}
