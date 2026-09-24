import { useCallback, useEffect, useMemo, useState } from 'react';
import { Box, CircularProgress, Stack, Typography } from '@mui/material';
import { ListingLayout } from '@ui/ListingLayout';
import { Button as DsButton } from '@ui/Button';
import { Input } from '@ui/Input';
import { Label } from '@ui/Label';
import { Modal } from '@ui/Modal';
import { toast as snackbar } from '@ui/Toast';
import CustomTable from '@shared/tables/CustomTable';
import ThreeDotsMenu from '@ui/ThreeDotsMenu';
import Text from '@shared/format/Text';
import Datetime from '@shared/format/Datetime';
import apiIntegrations from '@api1/integrations';
import { canManage } from '@lib/auth';
import { action } from 'src/utils/actionStyles';
import { ds } from 'src/utils/colors';

const WEBHOOK_TYPE = 'slack_webhook';

// GraphQL actions resolve (not throw) on backend errors — inspect before treating a write as done.
function gqlErrorOf(response) {
  return response?.errors?.[0]?.message || response?.data?.errors?.[0]?.message || response?.message || '';
}

/**
 * Slack incoming channel webhook targets. Many per tenant, each a
 * slack_webhook integration row with the URL encrypted at rest. Its own
 * section under the Slack tile because that tile's table assumes one install.
 */
export default function SlackWebhookTargets() {
  const userCanManage = canManage('messagingplatforms', 'Write');

  const [targets, setTargets] = useState([]);
  const [loading, setLoading] = useState(true);
  const [busyId, setBusyId] = useState('');
  const [toDelete, setToDelete] = useState(null);

  // Create / edit modal
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState(null); // the row being edited, or null for create
  const [nameVal, setNameVal] = useState('');
  const [urlVal, setUrlVal] = useState('');
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  // The URL is the only thing we can verify, so a new/changed URL must pass the
  // live test before it can be saved. 'idle' | 'ok' | 'failed'.
  const [testStatus, setTestStatus] = useState('idle');

  const loadTargets = useCallback(async () => {
    try {
      const res = await apiIntegrations.listIntegrations({ type: WEBHOOK_TYPE, limit: 200 });
      const rows = res?.data?.data?.integrations_list?.rows || [];
      setTargets(
        rows.map((row) => ({
          id: row.id,
          name: row.name,
          createdBy: row.created_by_display_name || '—',
          createdAt: row.created_at,
          status: row.status,
        }))
      );
    } catch (err) {
      console.error('Failed to load Slack webhook targets:', err);
      setTargets([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadTargets();
  }, [loadTargets]);

  const openCreate = () => {
    setEditing(null);
    setNameVal('');
    setUrlVal('');
    setTestStatus('idle');
    setModalOpen(true);
  };

  const openEdit = (row) => {
    setEditing(row);
    setNameVal(row.name);
    setUrlVal(''); // URL is a stored secret — blank means "keep the existing one"
    setTestStatus('idle');
    setModalOpen(true);
  };

  const closeModal = () => {
    setModalOpen(false);
    setEditing(null);
    setTestStatus('idle');
  };

  // Editing the URL invalidates any earlier test.
  const handleUrlChange = (v) => {
    setUrlVal(v);
    setTestStatus('idle');
  };

  // Name is the update/delete key (backend looks up by type+name), so it must be unique.
  const nameClashes = (name) =>
    targets.some((t) => (typeof t.name === 'string' ? t.name : '').trim().toLowerCase() === name.trim().toLowerCase() && t.id !== editing?.id);

  const buildConfigValues = () => {
    const values = [];
    if (urlVal.trim()) values.push({ name: 'webhook_url', value: urlVal.trim() });
    return values;
  };

  const handleTest = async () => {
    if (!urlVal.trim()) {
      snackbar.error('Enter the webhook URL to test it.');
      return;
    }
    setTesting(true);
    try {
      // Name (passed as integration_config_name) lets the test message say which target it came from.
      const res = await apiIntegrations.testIntegrationConnectionByConfig(WEBHOOK_TYPE, [], buildConfigValues(), 'user', editing?.id, nameVal.trim());
      if (res?.success) {
        setTestStatus('ok');
        snackbar.success('Test message posted to the Slack channel.');
      } else {
        setTestStatus('failed');
        snackbar.error(res?.error || 'The webhook did not accept the test message.');
      }
    } finally {
      setTesting(false);
    }
  };

  // A newly entered URL needs a passing test; an edit that keeps the stored URL
  // (blank field) does not — it was verified at creation.
  const urlNeedsTest = !editing || !!urlVal.trim();
  const canSave = !!nameVal.trim() && (!urlNeedsTest || testStatus === 'ok') && (!!editing || !!urlVal.trim());

  const handleSave = async () => {
    const name = nameVal.trim();
    if (!name) {
      snackbar.error('Give this webhook target a name.');
      return;
    }
    if (nameClashes(name)) {
      snackbar.error('A webhook target with this name already exists.');
      return;
    }
    if (!editing && !urlVal.trim()) {
      snackbar.error('Paste the webhook URL from Slack.');
      return;
    }
    if (urlNeedsTest && testStatus !== 'ok') {
      snackbar.error('Send a test message and get a successful result before saving.');
      return;
    }
    // Rename-only edit (URL field blank): nothing to validate, and the backend's
    // required-field check would reject the payload for the missing webhook_url.
    const renameOnly = !!editing?.id && !urlVal.trim();
    setSaving(true);
    try {
      const res = await apiIntegrations.addIntegrations({
        integration_name: WEBHOOK_TYPE,
        integration_config_name: name,
        integration_config_values: buildConfigValues(),
        account_ids: [],
        source: 'user',
        // integration_id -> update path (create would reject the existing name).
        ...(editing?.id ? { integration_id: editing.id } : {}),
        ...(renameOnly ? { skip_validation: true } : {}),
      });
      const gqlError = gqlErrorOf(res);
      if (gqlError) {
        snackbar.error(gqlError);
        return;
      }
      if (res?.data?.data?.integrations_create_config?.id) {
        snackbar.success(editing ? 'Webhook target updated.' : 'Webhook target created.');
        closeModal();
        await loadTargets();
      } else {
        snackbar.error('Failed to save the webhook target.');
      }
    } catch (err) {
      snackbar.error(err?.message || 'Failed to save the webhook target.');
    } finally {
      setSaving(false);
    }
  };

  const handleTestRow = async (row) => {
    setBusyId(row.id);
    try {
      const res = await apiIntegrations.testIntegrationConnection(row.id);
      if (res?.success) {
        snackbar.success(`Test message posted via “${row.name}”.`);
      } else {
        snackbar.error(res?.error || 'The webhook did not accept the test message.');
      }
    } finally {
      setBusyId('');
    }
  };

  const handleDelete = async () => {
    if (!toDelete) return;
    setBusyId(toDelete.id);
    try {
      const res = await apiIntegrations.deleteIntegrations({
        integration_name: WEBHOOK_TYPE,
        integration_config_name: toDelete.name,
        source: 'user',
      });
      const gqlError = gqlErrorOf(res);
      if (gqlError) {
        snackbar.error(gqlError);
      } else {
        snackbar.success(`Deleted “${toDelete.name}”.`);
      }
      await loadTargets();
    } catch (err) {
      snackbar.error(err?.message || 'Failed to delete the webhook target.');
    } finally {
      setBusyId('');
      setToDelete(null);
    }
  };

  const getMenuItems = () => {
    if (!userCanManage) return [];
    return [
      { label: 'Test', id: 'test' },
      { label: 'Edit', id: 'edit' },
      { label: 'Delete', id: 'delete' },
    ];
  };

  const onMenuClick = (menuItem, row) => {
    if (menuItem.id === 'test') handleTestRow(row);
    else if (menuItem.id === 'edit') openEdit(row);
    else if (menuItem.id === 'delete') setToDelete(row);
  };

  const headers = ['Name', 'Added by', 'Added at', 'Status', ''];

  const tableData = useMemo(
    () =>
      targets.map((row) => [
        { component: <Text value={row.name} /> },
        { component: <Text value={row.createdBy} /> },
        { component: row.createdAt && <Datetime value={row.createdAt} /> },
        {
          component: (
            <Label tone={row.status === 'disabled' ? 'neutral' : 'success'} text={row.status === 'disabled' ? 'Disabled' : 'Active'} size='sm' />
          ),
        },
        {
          component:
            busyId === row.id ? (
              <CircularProgress size={16} />
            ) : userCanManage ? (
              <ThreeDotsMenu sx={{ ...action.primary }} menuItems={getMenuItems()} data={row} onMenuClick={onMenuClick} />
            ) : (
              <></>
            ),
        },
      ]),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [targets, busyId, userCanManage]
  );

  return (
    <>
      <ListingLayout id='slack-webhook-targets'>
        <ListingLayout.Toolbar
          actions={
            userCanManage ? (
              <DsButton id='create-slack-webhook-btn' tone='primary' size='md' onClick={openCreate}>
                Create webhook
              </DsButton>
            ) : undefined
          }
        >
          <Box display='flex' flexDirection='column'>
            <Typography sx={{ fontFamily: 'var(--ds-font-display)' }} fontSize={ds.text.title} fontWeight={ds.weight.semibold} color={ds.gray[700]}>
              Webhook targets
            </Typography>
            <Typography fontSize={ds.text.caption} color={ds.gray[600]}>
              Post notifications to a Slack channel through an Incoming Webhook — no app install or admin approval. One-way only: no chat, replies,
              reactions, or interactive buttons. Route a notification rule to a target to use it.
            </Typography>
          </Box>
        </ListingLayout.Toolbar>
        <ListingLayout.Body>
          <CustomTable id='slack-webhook' headers={headers} tableData={tableData} loading={loading} />
        </ListingLayout.Body>
      </ListingLayout>

      <Modal
        open={modalOpen}
        onClose={closeModal}
        title={editing ? 'Edit webhook target' : 'Create webhook target'}
        confirmText={editing ? 'Save' : 'Create'}
        cancelText='Cancel'
        loader={saving}
        confirmDisabled={!canSave}
        onConfirm={handleSave}
      >
        <Stack spacing={ds.space[3]}>
          <Input id='slack-webhook-name' label='Name' required value={nameVal} onChange={setNameVal} placeholder='e.g. Prod alerts — #ops' />
          <Input
            id='slack-webhook-url'
            label='Webhook URL'
            required={!editing}
            value={urlVal}
            onChange={handleUrlChange}
            placeholder={editing ? 'Leave blank to keep the existing URL' : 'https://hooks.slack.com/services/… (from Incoming Webhooks)'}
          />
          <Typography fontSize={ds.text.caption} color={ds.gray[600]}>
            In Slack: api.slack.com/apps → your app → Incoming Webhooks → “Add New Webhook to Workspace”, pick the channel. Paste the URL it gives
            you.
          </Typography>
          <Stack direction='row' spacing={ds.space[2]} alignItems='center'>
            <DsButton id='slack-webhook-test-btn' tone='secondary' size='sm' loading={testing} disabled={!urlVal.trim()} onClick={handleTest}>
              Send test message
            </DsButton>
            {testStatus === 'ok' && (
              <Typography fontSize={ds.text.caption} sx={{ color: 'var(--ds-green-600)' }}>
                ✓ Test delivered — you can {editing ? 'save' : 'create'} this target.
              </Typography>
            )}
            {testStatus === 'failed' && (
              <Typography fontSize={ds.text.caption} sx={{ color: 'var(--ds-red-600)' }}>
                Test failed — fix the URL and retry.
              </Typography>
            )}
          </Stack>
          {urlNeedsTest && testStatus !== 'ok' && (
            <Typography fontSize={ds.text.caption} color={ds.gray[600]}>
              {editing ? 'Send a successful test to save the new URL.' : 'Send a successful test message to enable Create.'}
            </Typography>
          )}
        </Stack>
      </Modal>

      <Modal
        open={!!toDelete}
        onClose={() => setToDelete(null)}
        title='Delete webhook target?'
        subtitle={toDelete ? toDelete.name : ''}
        confirmText='Delete'
        confirmTone='danger'
        cancelText='Cancel'
        loader={!!busyId}
        onConfirm={handleDelete}
      >
        <Typography variant='body2'>
          Notification rules routed to this target will stop delivering to it. This does not affect the Slack channel or the Incoming Webhook.
        </Typography>
      </Modal>
    </>
  );
}
