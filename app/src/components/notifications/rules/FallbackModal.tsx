import React, { useEffect, useMemo, useState } from 'react';
import { Box, Stack, Typography } from '@mui/material';
import { Modal } from '@ui/Modal';
import { Button } from '@ui/Button';
import { Select } from '@ui/Select';
import { toast } from '@ui/Toast';
import SafeIcon from '@shared/icons/SafeIcon';
import apiNotifications from '@api1/notification';
import { ds } from '@utils/colors';
import { FALLBACK_TYPE_KEYS, RULE_TYPE, RuleTypeKey } from './catalogue';
import { Destination, buildMappings, valuesFromMappings, withSavedChannels } from './destinations';
import DestinationFields from './DestinationFields';
import { FallbackKey, FallbackMode, Fallbacks, fallbackRequest, sameFallback } from './fallback';

interface Props {
  open: boolean;
  fallbacks: Fallbacks;
  focusType: RuleTypeKey | null;
  destinations: Destination[];
  onClose: () => void;
  onSaved: () => void;
}

type Draft = Record<FallbackKey, { mode: FallbackMode; values: Record<string, string> }>;

const ALL_MODES = [
  { value: 'defaults', label: 'Send to each app’s default channel' },
  { value: 'channels', label: 'Send to specific channels' },
  { value: 'drop', label: 'Don’t send' },
];
const TYPE_MODES = [
  { value: 'inherit', label: 'Same as all notifications' },
  { value: 'defaults', label: 'Each app’s default channel' },
  { value: 'channels', label: 'Specific channels' },
  { value: 'drop', label: 'Don’t send' },
];

const KEYS: FallbackKey[] = ['all', ...FALLBACK_TYPE_KEYS];

const FallbackModal: React.FC<Props> = ({ open, fallbacks, focusType, destinations, onClose, onSaved }) => {
  const [draft, setDraft] = useState<Draft | null>(null);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);

  // Filled when the dialog opens only: after a partly failed save the page reloads
  // the saved settings, and the form keeps what is still to be saved.
  useEffect(() => {
    if (!open) return;
    setDraft(Object.fromEntries(KEYS.map((k) => [k, { mode: fallbacks[k].mode, values: valuesFromMappings(fallbacks[k].mappings) }])) as Draft);
    setError('');
  }, [open]);

  const shown = useMemo(
    () =>
      withSavedChannels(
        destinations,
        KEYS.flatMap((k) => fallbacks[k].mappings)
      ),
    [destinations, fallbacks]
  );

  if (!open || !draft) return null;

  const set = (key: FallbackKey, patch: Partial<Draft[FallbackKey]>) => {
    setDraft({ ...draft, [key]: { ...draft[key], ...patch } });
    setError('');
  };

  const save = async () => {
    const changes = [];
    for (const key of KEYS) {
      const { mode, values } = draft[key];
      const { mappings, problem } = mode === 'channels' ? buildMappings(shown, values) : { mappings: [], problem: undefined };
      const name = key === 'all' ? 'all notifications' : RULE_TYPE[key].label;
      if (problem) return setError(`${name}: ${problem}`);
      if (mode === 'channels' && !mappings.length) return setError(`Pick at least one channel for ${name}.`);
      const next = { mode, mappings };
      if (!sameFallback(next, fallbacks[key])) changes.push(fallbackRequest(key, next));
    }
    setSaving(true);
    for (const change of changes) {
      const res = await apiNotifications.upsertDefaultPolicy(change);
      if (res.error) {
        setSaving(false);
        setError(res.error);
        onSaved();
        return;
      }
    }
    setSaving(false);
    if (changes.length) toast.success('Saved');
    onSaved();
    onClose();
  };

  const channelsFor = (key: FallbackKey) =>
    draft[key].mode === 'channels' && (
      <Box sx={{ mt: ds.space[2] }}>
        <DestinationFields idPrefix={`fallback-${key}`} destinations={shown} value={draft[key].values} onChange={(values) => set(key, { values })} />
      </Box>
    );

  return (
    <Modal
      open={open}
      handleClose={onClose}
      title='When no rule matches'
      subtitle='What happens to notifications that no rule covers.'
      width='md'
      loader={saving}
      backdropClickClose={!saving}
      actionButtons={
        <Stack direction='row' alignItems='center' gap={ds.space[3]} sx={{ width: '100%' }}>
          <Typography role='alert' sx={{ flex: 1, fontSize: ds.text.small, color: ds.red[600] }}>
            {error}
          </Typography>
          <Button id='cancel-fallback' tone='secondary' onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button id='save-fallback' onClick={save} disabled={saving}>
            Save
          </Button>
        </Stack>
      }
    >
      <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[4], padding: ds.space[1] }}>
        <Box>
          <Select
            id='fallback-all'
            clearable={false}
            label='All notifications'
            size='sm'
            value={draft.all.mode}
            options={ALL_MODES}
            onChange={(mode) => set('all', { mode: mode as FallbackMode })}
          />
          {draft.all.mode === 'drop' && (
            <Typography sx={{ mt: ds.space[1], fontSize: ds.text.small, color: ds.gray[600] }}>
              Only your rules send anything. Approval requests always go out.
            </Typography>
          )}
          {channelsFor('all')}
        </Box>
        <Box>
          <Typography
            sx={{ fontFamily: ds.font.display, fontSize: ds.text.body, fontWeight: ds.weight.semibold, color: ds.gray[700], mb: ds.space[2] }}
          >
            Different for a type
          </Typography>
          <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[2] }}>
            {FALLBACK_TYPE_KEYS.map((key) => (
              <Box
                key={key}
                sx={{
                  padding: ds.space[2],
                  borderRadius: ds.radius.lg,
                  backgroundColor: focusType === key ? ds.blue[100] : 'transparent',
                }}
              >
                <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: '160px 1fr' }, gap: ds.space[2], alignItems: 'center' }}>
                  <Box sx={{ display: 'inline-flex', alignItems: 'center', gap: ds.space[1], fontSize: ds.text.body, color: ds.gray[700] }}>
                    <SafeIcon src={RULE_TYPE[key].icon} alt='' width={14} height={14} />
                    {RULE_TYPE[key].label}
                  </Box>
                  <Select
                    id={`fallback-${key}`}
                    clearable={false}
                    size='sm'
                    value={draft[key].mode}
                    options={TYPE_MODES}
                    onChange={(mode) => set(key, { mode: mode as FallbackMode })}
                  />
                </Box>
                {channelsFor(key)}
              </Box>
            ))}
          </Box>
          <Typography sx={{ mt: ds.space[2], fontSize: ds.text.small, color: ds.gray[600] }}>
            When a rule sends a notification, apps the rule doesn’t name also get it in their default channel only while this says each app’s default
            channel. Approval requests always go out.
          </Typography>
        </Box>
      </Box>
    </Modal>
  );
};

export default FallbackModal;
