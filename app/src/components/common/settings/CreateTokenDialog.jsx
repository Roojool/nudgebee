import React from 'react';
import { Box, Typography } from '@mui/material';
import { Modal } from '@ui/Modal';
import { Button } from '@ui/Button';
import { Input } from '@ui/Input';
import { Select } from '@ui/Select';
import { ds } from 'src/utils/colors';
import dayjs from 'dayjs';
import { carriesGatewayTraffic, KIND_LABELS } from './keyKinds';

const EXPIRY_OPTIONS = [
  { label: '30 days', value: '30' },
  { label: '90 days', value: '90' },
  { label: '365 days', value: '365' },
  { label: 'Never', value: 'never' },
  { label: 'Custom…', value: 'custom' },
];

const CUSTOM_UNIT_OPTIONS = ['minutes', 'hours', 'days'];
const CUSTOM_UNIT_MS = { minutes: 60 * 1000, hours: 60 * 60 * 1000, days: 24 * 60 * 60 * 1000 };

// Empty, zero, negative or non-numeric all fail this — the caller must never build a
// timestamp (or enable Create) from an unusable custom value.
const isValidCustomDuration = (value) => {
  const n = Number(value);
  return value !== '' && Number.isFinite(n) && n > 0;
};

const METRICS = ['requests', 'tokens', 'cost'];
const PERIODS = ['minute', 'hour', 'day', 'month'];
const EMPTY_LIMIT = { metric: 'cost', period: 'month', value: '', model: '' };

// `model` is free text for now: which catalog supplies the names is an open question in
// the spec, so no dropdown is wired up here.
const LimitEditor = ({ limits, onChange }) => {
  const update = (i, patch) => onChange(limits.map((l, n) => (n === i ? { ...l, ...patch } : l)));
  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[2] }}>
      {limits.map((l, i) => (
        <Box key={i} sx={{ display: 'flex', gap: ds.space[2], alignItems: 'flex-end' }}>
          <Select label='Metric' value={l.metric} onChange={(v) => update(i, { metric: v })} options={METRICS} />
          <Select label='Per' value={l.period} onChange={(v) => update(i, { period: v })} options={PERIODS} />
          <Input size='sm' label='Value' type='number' value={l.value} onChange={(v) => update(i, { value: v })} />
          <Input size='sm' label='Model (blank = any)' value={l.model} onChange={(v) => update(i, { model: v })} />
          <Button tone='secondary' size='sm' onClick={() => onChange(limits.filter((_, n) => n !== i))}>
            Remove
          </Button>
        </Box>
      ))}
      <Box>
        <Button tone='secondary' size='sm' onClick={() => onChange([...limits, { ...EMPTY_LIMIT }])}>
          Add limit
        </Button>
      </Box>
    </Box>
  );
};

// Platform keys carry an expiry and nothing else; the server 400s on limits it cannot
// meter, so unusable rows are dropped here rather than sent to be rejected.
const CreateTokenDialog = ({ open, kind, onClose, onCreate }) => {
  const [name, setName] = React.useState('');
  const [expiryDays, setExpiryDays] = React.useState('90');
  const [customValue, setCustomValue] = React.useState('');
  const [customUnit, setCustomUnit] = React.useState('hours');
  const [limits, setLimits] = React.useState([]);

  const reset = () => {
    setName('');
    setExpiryDays('90');
    setCustomValue('');
    setCustomUnit('hours');
    setLimits([]);
  };

  const handleClose = () => {
    reset();
    onClose();
  };

  const customValid = isValidCustomDuration(customValue);
  // Same computation submit() uses, for the "expires <date>" preview next to the inputs —
  // kept in sync by construction rather than duplicated at two different points in time.
  const customExpiresAt = customValid ? new Date(Date.now() + Number(customValue) * CUSTOM_UNIT_MS[customUnit]).toISOString() : null;

  const expiresAt = (() => {
    if (expiryDays === 'never') return null;
    if (expiryDays === 'custom') return customExpiresAt;
    return new Date(Date.now() + Number(expiryDays) * 86400000).toISOString();
  })();

  const submit = () => {
    // Held as a string while editing so the field can be cleared; coerced here because
    // the API takes a number.
    const usable = limits.filter((l) => Number(l.value) > 0).map((l) => ({ ...l, value: Number(l.value) }));
    onCreate({ name: name.trim(), kind, expiresAt, limits: carriesGatewayTraffic(kind) ? usable : [] });
    reset();
  };

  const createDisabled = !name.trim() || (expiryDays === 'custom' && !customValid);
  const title = `New ${KIND_LABELS[kind]} key`;

  return (
    <Modal
      open={open}
      onClose={handleClose}
      title={title}
      width='md'
      actionButtons={
        <Box sx={{ display: 'flex', gap: ds.space[3], justifyContent: 'flex-end' }}>
          <Button tone='secondary' onClick={handleClose}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={createDisabled}>
            Create
          </Button>
        </Box>
      }
    >
      <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[4] }}>
        <Input size='sm' label='Name' value={name} placeholder='Enter a descriptive name for your token' onChange={setName} />
        <Select label='Expires' value={expiryDays} onChange={setExpiryDays} options={EXPIRY_OPTIONS} />
        {expiryDays === 'custom' && (
          <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[1] }}>
            <Box sx={{ display: 'flex', gap: ds.space[2] }}>
              <Input size='sm' label='Duration' type='number' value={customValue} onChange={setCustomValue} />
              <Select label='Unit' value={customUnit} onChange={setCustomUnit} options={CUSTOM_UNIT_OPTIONS} />
            </Box>
            <Typography sx={{ fontSize: ds.text.small, color: ds.gray[500] }}>
              {customValid ? `expires ${dayjs(customExpiresAt).format('D MMM YYYY, HH:mm')}` : 'Enter a duration greater than zero'}
            </Typography>
          </Box>
        )}
        {kind === 'all' && (
          <Typography sx={{ fontSize: ds.text.small, color: ds.gray[500] }}>
            Opens both the app API and the AI Gateway. Limits below meter AI Gateway calls only — app API calls are not metered and pass freely.
          </Typography>
        )}
        {carriesGatewayTraffic(kind) && <LimitEditor limits={limits} onChange={setLimits} />}
      </Box>
    </Modal>
  );
};

export default CreateTokenDialog;
