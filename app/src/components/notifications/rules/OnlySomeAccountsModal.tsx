import React, { useEffect, useMemo, useState } from 'react';
import { Box, Stack, Typography } from '@mui/material';
import { Modal } from '@ui/Modal';
import { Button } from '@ui/Button';
import { Select } from '@ui/Select';
import { Chip } from '@ui/Chip';
import { toast } from '@ui/Toast';
import SafeIcon from '@shared/icons/SafeIcon';
import apiNotifications from '@api1/notification';
import { ds } from '@utils/colors';
import { RULE_TYPE, RuleTypeKey } from './catalogue';
import { Rule, cleanRuleName, listText, mappingLabel, newRule, toUpsertInput } from './model';
import { Destination, buildMappings } from './destinations';
import DestinationFields from './DestinationFields';
import { RuleAccount } from './RuleEditorModal';

interface Props {
  open: boolean;
  accounts: RuleAccount[];
  destinations: Destination[];
  rules: Rule[];
  onClose: () => void;
  onSaved: () => void;
}

const KINDS: RuleTypeKey[] = ['troubleshoot', 'slo', 'cloud', 'optimize'];

// A name no other rule has.
const freeName = (name: string, taken: Set<string>) => {
  let candidate = name;
  for (let n = 2; taken.has(candidate.toLowerCase()); n++) candidate = `${name} ${n}`;
  return candidate;
};

// "Only get alerts from some accounts": a Don't send for every other account and
// a Send for the kept ones, as two ordinary rules.
const OnlySomeAccountsModal: React.FC<Props> = ({ open, accounts, destinations, rules, onClose, onSaved }) => {
  const [types, setTypes] = useState<RuleTypeKey[]>(['troubleshoot', 'slo', 'cloud']);
  const [keep, setKeep] = useState<string[]>([]);
  const [values, setValues] = useState<Record<string, string>>({});
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  // The rules already created, so a retry after a failure doesn't create them twice.
  const [created, setCreated] = useState<string[]>([]);

  useEffect(() => {
    if (!open) return;
    setTypes(['troubleshoot', 'slo', 'cloud']);
    setKeep([]);
    setValues({});
    setError('');
    setCreated([]);
  }, [open]);

  const names = useMemo(() => new Map(accounts.map((a) => [a.id, a.name])), [accounts]);
  if (!open) return null;

  const nouns = listText(types.map((t) => RULE_TYPE[t].noun));
  const keepText = listText(keep.map((id) => names.get(id) || id)) || '…';
  const { mappings } = buildMappings(destinations, values);
  const toText = listText(mappings.map(mappingLabel)) || '…';

  const toggle = (key: RuleTypeKey) => {
    setTypes(types.includes(key) ? (types.length > 1 ? types.filter((t) => t !== key) : types) : [...types, key]);
  };

  const save = async () => {
    if (!keep.length) return setError('Pick at least one account to keep.');
    const built = buildMappings(destinations, values);
    if (built.problem) return setError(built.problem);
    if (!built.mappings.length) return setError('Pick at least one channel.');
    const taken = new Set(rules.map((r) => r.name.toLowerCase()));
    const silence = newRule({
      name: freeName(cleanRuleName(`Silence ${nouns} except ${keepText}`), taken),
      types,
      except: keep,
      action: 'drop',
    });
    taken.add(silence.name.toLowerCase());
    const send = newRule({
      name: freeName(cleanRuleName(`${keepText} ${nouns} to ${mappingLabel(built.mappings[0]).replace(/^#/, '')}`), taken),
      types,
      scope: 'some',
      accounts: keep,
      mappings: built.mappings,
    });
    setSaving(true);
    // The Send rule first: if the Don't send were saved alone, the kept accounts would go quiet too.
    const steps: [string, typeof send][] = [
      ['send', send],
      ['silence', silence],
    ];
    const done = [...created];
    for (const [key, rule] of steps) {
      if (done.includes(key)) continue;
      const res = await apiNotifications.upsertRule(toUpsertInput(rule));
      if (res.error) {
        setSaving(false);
        setCreated(done);
        setError(res.error);
        onSaved();
        return;
      }
      done.push(key);
    }
    setSaving(false);
    toast.success('2 rules created');
    onSaved();
    onClose();
  };

  return (
    <Modal
      open={open}
      handleClose={onClose}
      title='Only get alerts from some accounts'
      subtitle='Everything else stays quiet. Creates two rules you can edit later.'
      width='md'
      loader={saving}
      backdropClickClose={!saving}
      actionButtons={
        <Stack direction='row' alignItems='center' gap={ds.space[3]} sx={{ width: '100%' }}>
          <Typography role='alert' sx={{ flex: 1, fontSize: ds.text.small, color: ds.red[600] }}>
            {error}
          </Typography>
          <Button id='cancel-only-accounts' tone='secondary' onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button id='save-only-accounts' onClick={save} disabled={saving}>
            Create 2 rules
          </Button>
        </Stack>
      }
    >
      <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[4], padding: ds.space[1] }}>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[2] }}>
          <Typography sx={{ fontFamily: ds.font.display, fontSize: ds.text.small, fontWeight: ds.weight.medium, color: ds.gray[700] }}>
            Which notifications
          </Typography>
          <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: ds.space[1] }}>
            {KINDS.map((key) => (
              <Chip
                key={key}
                id={`only-type-${key}`}
                variant='filter'
                size='sm'
                pressed={types.includes(key)}
                icon={<SafeIcon src={RULE_TYPE[key].icon} alt='' width={12} height={12} />}
                onClick={() => toggle(key)}
              >
                {RULE_TYPE[key].label}
              </Chip>
            ))}
          </Box>
        </Box>
        <Select
          id='only-keep-accounts'
          label='Keep these accounts'
          multiple
          grouped
          size='sm'
          placeholder='Choose accounts'
          value={keep}
          options={accounts.map((a) => ({ value: a.id, label: a.name, group: a.provider || 'Other' }))}
          onChange={setKeep}
        />
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[2] }}>
          <Typography sx={{ fontFamily: ds.font.display, fontSize: ds.text.small, fontWeight: ds.weight.medium, color: ds.gray[700] }}>
            Send them to
          </Typography>
          <DestinationFields idPrefix='only' destinations={destinations} value={values} onChange={setValues} />
        </Box>
        <Box
          sx={{ padding: ds.space[3], borderRadius: ds.radius.lg, backgroundColor: ds.background[200], fontSize: ds.text.body, color: ds.gray[700] }}
        >
          <Typography sx={{ fontFamily: ds.font.display, fontSize: ds.text.body, fontWeight: ds.weight.semibold, mb: ds.space[1] }}>
            This creates
          </Typography>
          <Box>
            1. <b>Don’t send</b>: {nouns.charAt(0).toUpperCase() + nouns.slice(1)} from all accounts except {keepText} are never sent.
          </Box>
          <Box>
            2. <b>Send</b>: {nouns.charAt(0).toUpperCase() + nouns.slice(1)} from {keepText} go to {toText}.
          </Box>
        </Box>
      </Box>
    </Modal>
  );
};

export default OnlySomeAccountsModal;
