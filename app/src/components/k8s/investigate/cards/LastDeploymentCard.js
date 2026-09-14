import React, { useRef, useState } from 'react';
import PropTypes from 'prop-types';
import Datetime from '@shared/format/Datetime';
import { Modal } from '@ui/Modal';
import { Box, Typography } from '@mui/material';
import { Divider } from '@ui/Divider';
import InvestigateResolution from '@components/k8s/investigate/InvestigateResolution';
import LastDeploymentIcon from '@assets/investigation/last-deployment.svg';
import { toast as snackbar } from '@ui/Toast';
import { safeJSONParse } from 'src/utils/common';
import apiKubernetes from '@api1/kubernetes';
import { DiffViewer } from '@ui/DiffViewer';
import { Button } from '@ui/Button';
import { hasWriteAccess } from '@lib/auth';
import { ds } from '@utils/colors';

// The dialog used to title itself with event.subject_name — the POD, which the revert restarts as a
// side effect rather than the object it changes. resource_name carries the real target as
// "deployment/<namespace>/<name>.yaml"; show the workload name, falling back to the raw value when
// the shape is not that triple.
const revertTargetName = (resourceName) => {
  if (typeof resourceName !== 'string' || !resourceName) return '';
  const withoutExt = resourceName.replace(/\.yaml$/i, '');
  const parts = withoutExt.split('/');
  return parts[parts.length - 1] || withoutExt;
};

const isSingleDiff = (e) => e?.type === 'diff' && e?.additional_info?.action_name !== 'deployment_history';

// foldRepeatedChanges groups an event's diff evidences by the object they changed. The collector now
// sends one block per object, but events recorded before that carry one block per edit — five
// rewrites of an operator-managed ConfigMap rendered five identical cards and five Revert rows.
// Blocks arrive newest first, so the first block of each object is the one kept; the rest are
// skipped and carried along as that card's earlier edits, diff included, so each one can still be
// opened. A block with no resource_name is never folded.
export const foldRepeatedChanges = (evidences) => {
  const skip = new Set();
  const changes = new Map();
  const firstIndexByObject = new Map();
  (Array.isArray(evidences) ? evidences : []).forEach((e, index) => {
    if (!isSingleDiff(e)) return;
    const key = typeof e?.data?.resource_name === 'string' ? e.data.resource_name.trim().toLowerCase() : '';
    if (!key) return;
    if (!firstIndexByObject.has(key)) {
      firstIndexByObject.set(key, index);
      changes.set(index, [{ start_at: e.start_at, diff: e.data }]);
      return;
    }
    skip.add(index);
    changes.get(firstIndexByObject.get(key)).push({ start_at: e.start_at, diff: e.data });
  });
  return { skip, changes };
};

const WORKLOAD_KINDS = ['deployment', 'statefulset', 'daemonset', 'rollout', 'replicaset'];

// canRevertDiff decides whether the "What you can do" list may offer to revert one diff block.
//
// The server reverts by writing the recorded old values onto the ALERT's workload. A ConfigMap or
// Ingress edit is not a change to that workload, so reverting it there is wrong, and an operator-
// managed ConfigMap would be rewritten again anyway. And an alert carrying several diff blocks must
// name the change it means (change_event_id): without one the server would undo all of them at once.
export const canRevertDiff = (evidence, event) => {
  const parts = String(evidence?.data?.resource_name || '')
    .replace(/\.yaml$/i, '')
    .split('/');
  if (parts.length !== 3 || !WORKLOAD_KINDS.includes(parts[0].toLowerCase())) return false;
  if (event?.service_key && `${parts[1]}/${parts[2]}` !== event.service_key) return false;
  if (evidence?.additional_info?.change_event_id) return true;
  return (event?.evidences || []).filter(isSingleDiff).length === 1;
};

// isRevertableChange decides whether to offer a revert for one recorded change.
//
// The field summary is NOT required. It is a display convenience the server started sending
// recently; evidence recorded before that has none, and gating the action on it meant the card
// listed changes while the page above said "No fixes are available". The revert itself is built
// server-side from the change's own event, and the dialog fetches and shows that change's real
// diff before anything is dispatched — so the summary's absence costs a preview, not safety.
//
// What the summary DOES buy is knowing a change was only a restart, which is not worth reverting.
// With no summary we cannot tell, so the option is offered and the operator sees the real diff in
// the dialog; the server still refuses a revert that would clobber a later change.
const isRevertableChange = (d) => !!d?.eventId && !(d.changedFields?.length > 0 && isRolloutOnlyChange(d.changedFields));

// hasRenderableHistory reports whether this event carries a deployment-history evidence with at
// least one change on it — i.e. whether the list card will render.
const hasRenderableHistory = (event) => {
  const evidence = (event?.evidences || []).find((e) => e?.additional_info?.action_name === 'deployment_history');
  if (!evidence) return false;
  const parsed = safeJSONParse(evidence.data);
  return Array.isArray(parsed?.deployments) && parsed.deployments.length > 0;
};

// Annotations that only mark a rollout. A change touching nothing but these restarted the
// workload; it did not alter its configuration. Reverting one removes the marker, undoes nothing,
// and writing to the pod template can trigger a further rollout — so the option is worse than
// useless, it is misleading. On dev these are the two most-changed paths of all (rollme 1146,
// restartedAt 239 over 7 days), so offering revert on them is the common case, not the edge.
const ROLLOUT_ONLY_ANNOTATIONS = [
  'kubectl.kubernetes.io/restartedAt',
  'rollme',
  'deployment.kubernetes.io/revision',
  'kubectl.kubernetes.io/last-applied-configuration',
];

const isRolloutOnlyKey = (key) => ROLLOUT_ONLY_ANNOTATIONS.some((a) => key === a || key.endsWith(`/${a}`) || key.endsWith(`.${a}`));

// A value may arrive as an object (raw evidence) or as a JSON string (the trimmed summary), and an
// annotations-map change carries the annotation names inside the value rather than in the path.
const annotationKeysOf = (value) => {
  let parsed = value;
  if (typeof value === 'string') {
    try {
      parsed = JSON.parse(value);
    } catch {
      return null;
    }
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null;
  return Object.keys(parsed);
};

// isRolloutOnlyChange reports whether EVERY path a change touched is rollout bookkeeping. Every —
// a change that bumped an image and also stamped restartedAt is a real change and stays revertible.
const isRolloutOnlyChange = (fields) => {
  if (!Array.isArray(fields) || fields.length === 0) return false;
  return fields.every((f) => {
    const path = typeof f?.path === 'string' ? f.path : '';
    if (!path) return false;
    if (isRolloutOnlyKey(path)) return true;
    // A bare `...metadata.annotations` path says only that some annotation moved; which one is in
    // the value.
    if (path.endsWith('metadata.annotations')) {
      const keys = [...(annotationKeysOf(f?.new) || []), ...(annotationKeysOf(f?.old) || [])];
      return keys.length > 0 && keys.every(isRolloutOnlyKey);
    }
    return false;
  });
};

// ChangedField renders one changed path as `old -> new`. A null value means the field
// did not exist on that side of the change: null old = the change added it, null new =
// the change removed it. Rendering either as an empty string would misstate what happened.
const changedValueLabel = (value, absentLabel) => {
  if (value === null || value === undefined) return absentLabel;
  return value === '' ? '(empty)' : value;
};

const ChangedField = ({ field }) => (
  <Box sx={{ marginBottom: 'var(--ds-space-2)' }}>
    <Typography sx={{ fontSize: 'var(--ds-text-small)', fontFamily: 'var(--ds-font-mono)', color: 'var(--ds-gray-700)' }}>{field.path}</Typography>
    <Box display={'flex'} flexDirection={'row'} alignItems={'center'} gap={ds.space.mul(0, 3)} flexWrap={'wrap'}>
      <Typography sx={{ fontSize: 'var(--ds-text-small)', fontFamily: 'var(--ds-font-mono)', color: 'var(--ds-red-600)', wordBreak: 'break-all' }}>
        {changedValueLabel(field.old, '(not set)')}
      </Typography>
      <Typography sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-gray-600)' }}>{'\u2192'}</Typography>
      <Typography sx={{ fontSize: 'var(--ds-text-small)', fontFamily: 'var(--ds-font-mono)', color: 'var(--ds-green-600)', wordBreak: 'break-all' }}>
        {changedValueLabel(field.new, '(removed)')}
      </Typography>
    </Box>
  </Box>
);

ChangedField.propTypes = {
  field: PropTypes.shape({
    path: PropTypes.string,
    old: PropTypes.string,
    new: PropTypes.string,
  }).isRequired,
};

// DeploymentHistoryContent lists every change recorded before the alert and lets the operator
// revert the one they choose. It owns the dialog state itself rather than routing through the
// page's single resolve slot, because that slot reverts the alert's own attached change and there
// is exactly one of it — the entire failure being fixed here is a page that could only ever act on
// that one change.
const DeploymentHistoryContent = ({ history, event, cardId }) => {
  const [revertChangeId, setRevertChangeId] = useState('');
  const [expandedId, setExpandedId] = useState('');
  const [diffState, setDiffState] = useState({ loading: false, error: '', old: '', new: '' });

  // Fetched per change rather than shipped with the list: the before/after manifests average
  // ~9.7KB each, and carrying them in this response is what drove services-server to OOM.
  // Each fetch claims a ticket, and only the newest ticket may write state. Toggling A then B then
  // A again can resolve out of order, and a late response would then paint one change's diff under
  // another change's row — showing the operator a diff that belongs to a different change is the
  // exact confusion this card exists to end, so it must not be reachable by clicking quickly.
  const diffRequestRef = useRef(0);

  const toggleDiff = (eventId) => {
    if (expandedId === eventId) {
      setExpandedId('');
      diffRequestRef.current += 1; // abandon any in-flight response for the row being collapsed
      return;
    }
    const ticket = diffRequestRef.current + 1;
    diffRequestRef.current = ticket;
    setExpandedId(eventId);
    setDiffState({ loading: true, error: '', old: '', new: '' });
    apiKubernetes
      .resolveEventRecord(eventId, event?.cloud_account_id)
      .then((res) => {
        if (diffRequestRef.current !== ticket) return;
        const changeEvent = res?.data?.events?.[0];
        const diff = (changeEvent?.evidences || []).find((e) => e?.type === 'diff')?.data;
        if (!diff?.old && !diff?.new) {
          setDiffState({ loading: false, error: 'No before/after manifest was recorded for this change.', old: '', new: '' });
          return;
        }
        setDiffState({ loading: false, error: '', old: diff.old || '', new: diff.new || '' });
      })
      .catch(() => {
        if (diffRequestRef.current !== ticket) return;
        setDiffState({ loading: false, error: 'That change could not be loaded.', old: '', new: '' });
      });
  };
  const { namespace, rolloutName, service, timeRangeHours, deployments } = history;
  const canRevert = hasWriteAccess();

  const handleSnackbar = (type, message) => {
    if (['success', 'error'].includes(type)) {
      snackbar[type](message);
    }
  };

  return (
    <Box>
      <Typography sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-gray-600)', marginBottom: 'var(--ds-space-2)' }}>
        {/* rollout_name is the workload the changes were made to; service_name is the alert's own
            subject, which for a pod-level alert is the pod. Naming the pod here says a pod was
            changed, which is not what happened. */}
        {`${deployments.length} change${deployments.length === 1 ? '' : 's'} to ${
          rolloutName || service
        } in ${namespace} over the ${timeRangeHours} hours before this alert`}
      </Typography>
      {deployments.map((d, index) => (
        <Box key={d.eventId || index} sx={{ paddingBottom: 'var(--ds-space-3)' }}>
          {/* One line per change: when it happened on the left, what you can do about it on the
              right. The actions used to stack under the row as full-height buttons with a sentence
              between them, which made three changes read as a wall of controls rather than a list
              of changes. */}
          <Box display={'flex'} flexDirection={'row'} justifyContent={'space-between'} alignItems={'center'} gap={ds.space.mul(0, 3)}>
            <Box display={'flex'} flexDirection={'row'} alignItems={'baseline'} gap={ds.space.mul(0, 2)} sx={{ minWidth: 0 }}>
              <Typography
                sx={{
                  color: 'var(--ds-gray-900)',
                  fontSize: 'var(--ds-text-body)',
                  fontWeight: 'var(--ds-font-weight-medium)',
                  whiteSpace: 'nowrap',
                }}
              >
                {d.timeBeforeEvent ? `${d.timeBeforeEvent} before` : 'Change'}
              </Typography>
              {d.timestamp && <Datetime value={d.timestamp} sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-gray-600)' }} />}
            </Box>

            <Box display={'flex'} flexDirection={'row'} alignItems={'center'} gap={ds.space.mul(0, 2)} sx={{ flexShrink: 0 }}>
              {d.eventId && (
                <Button size='xs' tone='link' onClick={() => toggleDiff(d.eventId)}>
                  {expandedId === d.eventId ? 'Hide diff' : 'Show diff'}
                </Button>
              )}
              {isRevertableChange(d) && (
                <Button size='xs' tone='secondary' disabled={!canRevert} onClick={() => setRevertChangeId(d.eventId)}>
                  {'Revert'}
                </Button>
              )}
              {/* Say "restart only" only where that is actually known. A change with no recorded id
                  is equally unrevertible, and labelling it a restart would be a claim we cannot
                  make from the data. */}
              {d.changedFields.length > 0 && isRolloutOnlyChange(d.changedFields) && (
                <Typography sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-gray-500)', whiteSpace: 'nowrap' }}>{'Restart only'}</Typography>
              )}
            </Box>
          </Box>

          {/* The changed fields, when the server recorded them. Nothing is said when it did not:
              "Show diff" already answers the question, and repeating an apology on every row was
              louder than the content. */}
          {d.changedFields.length > 0 && (
            <Box sx={{ marginTop: 'var(--ds-space-2)' }}>
              {d.changedFields.map((f, fieldIndex) => (
                <ChangedField key={`${f.path}_${fieldIndex}`} field={f} />
              ))}
              {d.fieldsOmitted > 0 && (
                <Typography sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-gray-600)' }}>
                  {`and ${d.fieldsOmitted} more field${d.fieldsOmitted === 1 ? '' : 's'} not shown`}
                </Typography>
              )}
              {/* A clipped value must never read as the whole value: acting on a half-rendered
                  container command is acting on something you have not seen. */}
              {d.valueTruncated && (
                <Typography sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-amber-600)' }}>
                  {'Long values are shortened — the diff and the revert dialog show them in full.'}
                </Typography>
              )}
            </Box>
          )}

          {expandedId === d.eventId && (
            <Box sx={{ marginTop: 'var(--ds-space-2)' }}>
              {diffState.error ? (
                <Typography sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-red-600)' }}>{diffState.error}</Typography>
              ) : diffState.loading ? (
                <Typography sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-gray-600)' }}>{'Loading the diff…'}</Typography>
              ) : (
                <DiffViewer originalCode={diffState.old} newCode={diffState.new} language='yaml' leftLabel='Before' rightLabel='After' />
              )}
            </Box>
          )}

          {index !== deployments.length - 1 && <Divider sx={{ marginTop: 'var(--ds-space-3)' }} />}
        </Box>
      ))}

      <Modal
        width='md'
        open={!!revertChangeId}
        handleClose={() => setRevertChangeId('')}
        title={`Revert a change to ${rolloutName || service || event?.subject_name}`}
        loader={false}
      >
        <InvestigateResolution
          accountId={event?.cloud_account_id ?? ''}
          row={event}
          handleClose={() => setRevertChangeId('')}
          updateInvestigateSuccessSnackBar={handleSnackbar}
          isRevertTheDevelopment={!!revertChangeId}
          cardId={cardId}
          changeEventId={revertChangeId}
        />
      </Modal>
    </Box>
  );
};

DeploymentHistoryContent.propTypes = {
  history: PropTypes.object.isRequired,
  event: PropTypes.object,
  cardId: PropTypes.string,
};

// changedPathsLabel names what an edit touched, so rows of one repeatedly-edited object can be told
// apart without opening each diff.
const changedPathsLabel = (diff) => {
  const paths = (Array.isArray(diff?.updated_values) ? diff.updated_values.map((v) => v?.path) : diff?.updated_paths || []).filter(Boolean);
  if (paths.length === 0) return '';
  const shown = paths.slice(0, 3).join(', ');
  return paths.length > 3 ? `${shown} and ${paths.length - 3} more` : shown;
};

// RepeatedChangesContent lists every edit folded into one card, newest first, each with its own diff.
// A card that showed only the newest diff and the other edits as bare times gave no way to see what
// those edits were. Diffs already on the page are shown directly; the collector keeps only the newest
// one per object, so older edits are fetched from their own change event on demand.
const RepeatedChangesContent = ({ changes, event }) => {
  const [expanded, setExpanded] = useState(0);
  // Keyed by change event id, so a slow response can only ever fill in its own row.
  const [fetched, setFetched] = useState({});

  const toggle = (index) => {
    if (expanded === index) {
      setExpanded(-1);
      return;
    }
    setExpanded(index);
    const change = changes[index];
    if (change.diff || !change.eventId || fetched[change.eventId]) return;
    setFetched((prev) => ({ ...prev, [change.eventId]: { loading: true } }));
    apiKubernetes
      .resolveEventRecord(change.eventId, event?.cloud_account_id)
      .then((res) => {
        const diff = (res?.data?.events?.[0]?.evidences || []).find((e) => e?.type === 'diff')?.data;
        setFetched((prev) => ({
          ...prev,
          [change.eventId]: diff?.old || diff?.new ? { diff } : { error: 'No before/after manifest was recorded for this change.' },
        }));
      })
      .catch(() => setFetched((prev) => ({ ...prev, [change.eventId]: { error: 'That change could not be loaded.' } })));
  };

  return (
    <Box sx={{ marginTop: 'var(--ds-space-2)' }}>
      {changes.map((change, index) => {
        const state = change.eventId ? fetched[change.eventId] : null;
        const diff = change.diff || state?.diff;
        const paths = changedPathsLabel(diff);
        return (
          <Box key={change.eventId || `${change.startAt}_${index}`} sx={{ paddingBlock: 'var(--ds-space-2)' }}>
            <Box display={'flex'} flexDirection={'row'} justifyContent={'space-between'} alignItems={'center'} gap={ds.space.mul(0, 3)}>
              <Box display={'flex'} flexDirection={'row'} alignItems={'baseline'} gap={ds.space.mul(0, 2)} sx={{ minWidth: 0, flexWrap: 'wrap' }}>
                <Typography sx={{ color: 'var(--ds-gray-900)', fontSize: 'var(--ds-text-body)', fontWeight: 'var(--ds-font-weight-medium)' }}>
                  {index === 0 ? 'Latest' : `Edit ${changes.length - index}`}
                </Typography>
                {change.startAt && <Datetime value={change.startAt} sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-gray-600)' }} />}
                {paths && (
                  <Typography
                    sx={{ fontSize: 'var(--ds-text-small)', fontFamily: 'var(--ds-font-mono)', color: 'var(--ds-gray-700)', wordBreak: 'break-all' }}
                  >
                    {paths}
                  </Typography>
                )}
              </Box>
              {(change.diff || change.eventId) && (
                <Button size='xs' tone='link' onClick={() => toggle(index)}>
                  {expanded === index ? 'Hide diff' : 'Show diff'}
                </Button>
              )}
            </Box>
            {expanded === index && (
              <Box sx={{ marginTop: 'var(--ds-space-2)' }}>
                {state?.error ? (
                  <Typography sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-red-600)' }}>{state.error}</Typography>
                ) : diff ? (
                  <DiffViewer originalCode={diff.old} newCode={diff.new} language='yaml' leftLabel='Before' rightLabel='After' />
                ) : (
                  <Typography sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-gray-600)' }}>{'Loading the diff…'}</Typography>
                )}
              </Box>
            )}
            {index !== changes.length - 1 && <Divider sx={{ marginTop: 'var(--ds-space-2)' }} />}
          </Box>
        );
      })}
    </Box>
  );
};

RepeatedChangesContent.propTypes = {
  changes: PropTypes.arrayOf(PropTypes.object).isRequired,
  event: PropTypes.object,
};

class LastDeploymentCard {
  // foldedChanges: every edit foldRepeatedChanges folded into this block ({start_at, diff}), newest first.
  constructor(evidenceData, event, index, foldedChanges) {
    this.id = `LastDeploymentCard_${index}`;
    this.foldedChanges = foldedChanges;
    this.icon = LastDeploymentIcon;
    this.text = evidenceData.additional_info?.title || 'Last Deployment Change';
    this.resolveButton = true;
    this.renderContent = false;
    this.highlightsData = [];
    this.diff = [];
    this.event = event;
    this.evidenceData = evidenceData;
    this.deploymentHistory = {};
  }

  canRenderContent = async () => {
    if (this.evidenceData?.additional_info?.action_name == 'deployment_history') {
      const parsedData = safeJSONParse(this.evidenceData.data);
      // Every deployment the server returned is rendered, including one whose changed
      // fields were not recorded. Dropping those used to hide the card entirely: the
      // server stopped sending per-deployment `evidences` in #27136 to stop an OOM,
      // and this branch required them, so the multi-change card rendered for nobody.
      // A deployment we cannot describe still happened, and saying so beats silence.
      const deployments = Array.isArray(parsedData?.deployments) ? parsedData.deployments : [];
      if (deployments.length > 0) {
        // An event can carry BOTH a single `diff` evidence and a `deployment_history` evidence,
        // and investigate.jsx builds one card per evidence — so both render, and both were titled
        // "Last Deployment Change". Two identically-titled cards on one page, one showing a single
        // change and the other several, reads as a duplicate rather than as two different things.
        this.text = this.evidenceData.additional_info?.title || 'Deployment Changes';
        this.deploymentHistory = {
          namespace: parsedData.namespace,
          rolloutName: parsedData.rollout_name,
          service: parsedData.service_name,
          timeRangeHours: parsedData.time_range_hours,
          deployments: deployments.map((d) => ({
            eventId: d.event_id,
            timestamp: d.timestamp,
            timeBeforeEvent: d.time_before_event,
            deploymentStrategy: d.deployment_strategy,
            changedFields: Array.isArray(d.updated_values) ? d.updated_values : [],
            fieldsOmitted: d.fields_omitted || 0,
            valueTruncated: !!d.value_truncated,
          })),
        };
        // The card-level action targets the most recent change that is actually revertible, and
        // the row names it. Suppressing the action outright — the first attempt at killing the
        // duplicate row — left the Remediation tab saying "No fixes are available" directly above
        // a card listing changes with Revert buttons on them. Naming the change is what removes
        // the ambiguity; hiding the action threw away the answer along with the question.
        this.primaryChange = this.deploymentHistory.deployments.find(isRevertableChange) || null;
        this.resolveButton = !!this.primaryChange;
        this.actionSubject = this.primaryChange?.timestamp ? new Date(this.primaryChange.timestamp).toLocaleString() : '';
        this.renderContent = true;
      }
      this.highlightsData = this.evidenceData.insight;
    } else {
      const diff = this.evidenceData.type === 'diff';
      if (diff) {
        // The history card lists every change in the window, including this one, and now shows its
        // diff in place — so rendering both produced two cards describing the same change, and an
        // action bar with two revert rows that could not be told apart. Stand down when a history
        // card will actually render; if it has nothing to show, this card is still the only view.
        if (hasRenderableHistory(this.event)) {
          this.resolveButton = false;
          return false;
        }
        this.renderContent = true;
        this.diff = this.evidenceData;
        this.highlightsData = this.evidenceData?.insight;
        // The collector folds repeat edits and says so in its title; for events recorded before
        // that, the page folds them and the title has to say it here. Built from the stored title
        // rather than appended to this.text: this runs more than once per card.
        const info = this.evidenceData.additional_info || {};
        this.changes = Array.isArray(info.changes)
          ? info.changes.map((c) => ({ startAt: c?.start_at, eventId: c?.change_event_id }))
          : (this.foldedChanges || []).map((c) => ({ startAt: c?.start_at, diff: c?.diff }));
        if (this.changes.length > 0) this.changes[0] = { ...this.changes[0], diff: this.evidenceData.data };
        const baseTitle = info.title || 'Last Deployment Change';
        this.text = !info.change_count && this.changes.length > 1 ? `${baseTitle} · ${this.changes.length} times` : baseTitle;
        this.revertChangeId = info.change_event_id || '';
        this.resolveButton = canRevertDiff(this.evidenceData, this.event);
        // Lets the action row name the change it reverts instead of "the last deployment change",
        // which says nothing on a workload that changed several times.
        this.actionSubject = this.evidenceData.start_at ? new Date(this.evidenceData.start_at).toLocaleString() : '';
      }
    }
    return this.renderContent;
  };

  getHighLightsData = () => {
    return this.highlightsData;
  };

  getContentComponents = () => {
    return [() => this.renderDiffData()];
  };

  renderDiffData = () => {
    if (Object.keys(this.diff).length > 0) {
      return (
        <Box>
          <Box display={'flex'} flexDirection={'row'} justifyContent={'space-between'} alignItems={'center'} marginTop={ds.space.mul(0, 10)}>
            <Typography
              display={'inline'}
              sx={{
                color: 'var(--ds-brand-500)',
                fontSize: 'var(--ds-text-body-lg) !important',
                fontWeight: 'var(--ds-font-weight-medium)',
              }}
            >
              {' Here’s What Changed'}
            </Typography>
            {this.diff.start_at && (
              <Box
                display={'flex'}
                flexDirection={'row'}
                alignItems={'center'}
                justifyContent={'center'}
                gap={ds.space.mul(0, 3)}
                sx={{
                  border: '1px solid var(--grey-40, var(--ds-brand-200))',
                  padding: 'var(--ds-space-1) var(--ds-space-1)',
                  borderRadius: 'var(--ds-radius-sm)',
                }}
              >
                <Typography
                  sx={{
                    color: 'var(--ds-brand-500)',
                    fontSize: 'var(--ds-text-body-lg) !important',
                    fontWeight: 'var(--ds-font-weight-medium)',
                  }}
                  display={'inline'}
                >
                  {'Deployed '}
                </Typography>
                <Datetime
                  value={this.diff.start_at}
                  sx={{
                    fontSize: 'var(--ds-text-body-lg) !important',
                    fontWeight: 'var(--ds-font-weight-medium)',
                    lineHeight: '16px',
                  }}
                />
              </Box>
            )}
          </Box>
          {this.changes?.length > 1 ? (
            <>
              <Typography sx={{ fontSize: 'var(--ds-text-small)', color: 'var(--ds-gray-600)', marginTop: 'var(--ds-space-2)' }}>
                {`Changed ${this.changes.length} times before this alert.`}
              </Typography>
              <RepeatedChangesContent changes={this.changes} event={this.event} />
            </>
          ) : (
            <Box sx={{ marginTop: 'var(--ds-space-3)', marginBottom: 'var(--ds-space-2)' }}>
              <DiffViewer originalCode={this.diff?.data?.old} newCode={this.diff?.data?.new} language='yaml' leftLabel='Before' rightLabel='After' />
            </Box>
          )}
        </Box>
      );
    } else if (this.deploymentHistory?.deployments?.length > 0) {
      return <DeploymentHistoryContent history={this.deploymentHistory} event={this.event} cardId={this.id} />;
    }
    return (
      <Typography marginTop={ds.space.mul(0, 5)} fontSize={ds.text.bodyLg} fontWeight={500}>
        No diff available.
      </Typography>
    );
  };

  RevertTheDeployment = (props) => {
    const handleSnackbar = (type, message) => {
      if (['success', 'error'].includes(type)) {
        snackbar[type](message);
      }
    };

    return (
      <Modal
        width='md'
        open={props.open}
        handleClose={props.onCloseComponent}
        title={`Revert the Deployment of ${
          revertTargetName(this.diff?.data?.resource_name) || this.deploymentHistory?.rolloutName || this.event?.subject_name
        }`}
        loader={false}
      >
        <InvestigateResolution
          accountId={this.event?.cloud_account_id ?? ''}
          row={this.event}
          handleClose={props.onCloseComponent}
          updateInvestigateSuccessSnackBar={handleSnackbar}
          isRevertTheDevelopment={props.open}
          cardId={this.id}
          changeEventId={this.primaryChange?.eventId || this.revertChangeId || ''}
        />
      </Modal>
    );
  };

  getResolveComponent = () => {
    // History card: revert the change the action row names.
    if (this.primaryChange) {
      return this.RevertTheDeployment;
    }
    // Same rule for the card-level action, which is what the "What you can do" bar offers: a
    // restart is not a change to put back.
    if (!canRevertDiff(this.diff, this.event) || isRolloutOnlyChange(this.diff?.data?.updated_values)) {
      this.resolveButton = false;
      return null;
    }
    if (this.diff?.data?.old && this.diff?.data?.new) {
      return this.RevertTheDeployment;
    }
    this.resolveButton = false;
    return null;
  };
}

export default LastDeploymentCard;
