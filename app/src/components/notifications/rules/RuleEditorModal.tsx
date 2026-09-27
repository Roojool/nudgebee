import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Box, Stack, Typography } from '@mui/material';
import dayjs from 'dayjs';
import { Modal } from '@ui/Modal';
import { Button } from '@ui/Button';
import { Input } from '@ui/Input';
import { Select } from '@ui/Select';
import { Chip } from '@ui/Chip';
import { Checkbox } from '@ui/Checkbox';
import { Callout } from '@ui/Callout';
import { ToggleGroup } from '@ui/ToggleGroup';
import { toast } from '@ui/Toast';
import CustomDateTimePicker from '@shared/widgets/CustomDateTimePicker';
import SafeIcon from '@shared/icons/SafeIcon';
import apiNotifications from '@api1/notification';
import apiKubernetes from '@api1/kubernetes';
import k8sApi from '@api1/kubernetes1';
import apiUser from '@api1/user';
import formatter from '@lib/formatter';
import { ds } from '@utils/colors';
import { RULE_TYPE, RULE_TYPES, RuleTypeKey } from './catalogue';
import {
  Rule,
  coversNoAccounts,
  eventTypeOf,
  formatWhen,
  hasWorkloadFilters,
  isEnded,
  isPaused,
  isValidRuleName,
  listText,
  onlyTroubleshooting,
  ruleIssues,
  suggestedName,
  toUpsertInput,
  typesOf,
} from './model';
import { Destination, buildMappings, valuesFromMappings, withSavedChannels } from './destinations';
import DestinationFields, { EDITOR_PICKER_IDS } from './DestinationFields';
import { RuleSentence, RuleStatusLabel } from './RuleParts';

export interface RuleAccount {
  id: string;
  name: string;
  provider: string;
}

export type PauseFor = 'always' | '1h' | '24h' | '7d' | 'date';

export interface EditorRequest {
  rule: Rule;
  isNew: boolean;
  // A new rule's name was typed (a copy), not suggested.
  named?: boolean;
  pauseFor?: PauseFor;
}

interface Props {
  request: EditorRequest | null;
  rules: Rule[];
  accounts: RuleAccount[];
  destinations: Destination[];
  onClose: () => void;
  onSaved: () => void;
}

const HOURS: Partial<Record<PauseFor, number>> = { '1h': 1, '24h': 24, '7d': 168 };
const isValidEmail = (email: string) => /^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$/.test(email);

const Section: React.FC<{ title: string; children: React.ReactNode }> = ({ title, children }) => (
  <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: '72px 1fr' }, gap: ds.space[3], alignItems: 'start' }}>
    <Typography sx={{ fontFamily: ds.font.display, fontSize: ds.text.body, fontWeight: ds.weight.semibold, color: ds.gray[700], pt: ds.space[1] }}>
      {title}
    </Typography>
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[2], minWidth: 0 }}>{children}</Box>
  </Box>
);

const Hint: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <Typography sx={{ fontSize: ds.text.small, color: ds.gray[600] }}>{children}</Typography>
);

const RuleEditorModal: React.FC<Props> = ({ request, rules, accounts, destinations, onClose, onSaved }) => {
  const open = !!request;
  const [draft, setDraft] = useState<Rule | null>(null);
  const [named, setNamed] = useState(false);
  const [pauseFor, setPauseFor] = useState<PauseFor>('always');
  const [pauseUntil, setPauseUntil] = useState<dayjs.Dayjs | null>(null);
  const [destValues, setDestValues] = useState<Record<string, string>>({});
  const [email, setEmail] = useState('');
  const [excluded, setExcluded] = useState<string[]>([]);
  const [users, setUsers] = useState<{ value: string; label: string }[]>([]);
  const [moreOpen, setMoreOpen] = useState(false);
  const [namespaces, setNamespaces] = useState<{ value: string; label: string }[]>([]);
  const [workloads, setWorkloads] = useState<{ value: string; label: string }[]>([]);
  const [alerts, setAlerts] = useState<{ value: string; label: string }[]>([]);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const alertRequest = useRef(0);

  useEffect(() => {
    if (!request) {
      setDraft(null);
      return;
    }
    const rule = request.rule;
    setDraft({ ...rule });
    setNamed(!request.isNew || !!request.named);
    const pausedDrop = rule.action === 'drop' && isPaused(rule);
    setPauseFor(request.pauseFor || (pausedDrop ? 'date' : 'always'));
    setPauseUntil(pausedDrop ? dayjs(rule.until) : null);
    setDestValues(valuesFromMappings(rule.mappings));
    const emailMapping = rule.mappings.find((m) => m.platform === 'email');
    const channels = emailMapping?.channels;
    setEmail(Array.isArray(channels) ? channels[0] || '' : channels?.emails?.[0] || '');
    setExcluded(Array.isArray(channels) ? [] : channels?.exclusion_emails || []);
    setMoreOpen(!!(rule.namespace || rule.workload || rule.alert || rule.severity || rule.description));
    setError('');
  }, [request]);

  const isDaily = draft?.types !== 'all' && draft?.types.length === 1 && draft.types[0] === 'daily_recap';

  useEffect(() => {
    if (!open || !isDaily || users.length) return;
    apiUser.listUsers({ status: 'active' }).then((res: any) => {
      const seen = new Set<string>();
      setUsers(
        (res?.data || [])
          .filter((u: any) => u.username && isValidEmail(u.username) && !seen.has(u.username) && seen.add(u.username))
          .map((u: any) => ({ value: u.username, label: u.display_name || u.username }))
      );
    });
  }, [open, isDaily]);

  const accountName = useMemo(() => {
    const names = new Map(accounts.map((a) => [a.id, a.name]));
    return (id: string) => names.get(id) || 'Unknown account';
  }, [accounts]);

  const scopedAccountIds = draft?.scope === 'some' ? draft.accounts : accounts.map((a) => a.id);
  const scopeKey = scopedAccountIds.join(',');
  const workloadFilters = !!draft && hasWorkloadFilters(draft.types);
  const alertFilters = !!draft && onlyTroubleshooting(draft.types);

  useEffect(() => {
    if (!open || !workloadFilters || !moreOpen) return;
    let active = true;
    apiKubernetes.getAllK8sNamespaces().then((res: any) => {
      if (!active) return;
      const wanted = new Set(scopedAccountIds);
      const names = new Set<string>((res?.data || []).filter((n: any) => wanted.has(n.cloud_account_id)).map((n: any) => n.namespace_name));
      setNamespaces(
        Array.from(names)
          .sort()
          .map((n) => ({ value: n, label: n }))
      );
    });
    return () => {
      active = false;
    };
  }, [open, workloadFilters, moreOpen, scopeKey]);

  useEffect(() => {
    if (!open || !workloadFilters || !draft?.namespace) {
      setWorkloads([]);
      return;
    }
    let active = true;
    apiKubernetes
      .getAllK8sWorkload({ namespace: draft.namespace, cloud_account_id: scopedAccountIds, workload_type: ['Deployment', 'Rollout'], isActive: true })
      .then((res: any) => {
        if (!active) return;
        const names = Array.from(new Set<string>((res?.data || []).map((w: any) => w.name)));
        setWorkloads(names.sort().map((n) => ({ value: n, label: n })));
      });
    return () => {
      active = false;
    };
  }, [open, workloadFilters, draft?.namespace, scopeKey]);

  useEffect(() => {
    if (!open || !alertFilters || !moreOpen) return;
    const current = ++alertRequest.current;
    // Alert names per account, for at most 20 accounts; other names can be typed.
    Promise.all(scopedAccountIds.slice(0, 20).map((id) => k8sApi.getEventRules({ accountId: id }, 500, 0))).then((responses: any[]) => {
      if (current !== alertRequest.current) return;
      const names = new Set<string>(
        responses.flatMap((r) => (r?.data?.event_rules || []).map((e: any) => e.alert).filter((a: any) => a && a.trim()))
      );
      setAlerts(
        Array.from(names)
          .sort()
          .map((n) => ({ value: n, label: n }))
      );
    });
  }, [open, alertFilters, moreOpen, scopeKey]);

  const shownDestinations = useMemo(() => withSavedChannels(destinations, request?.rule.mappings || []), [destinations, request]);

  const untilFor = (d: Rule): string | null => {
    if (d.action === 'drop') {
      const hours = HOURS[pauseFor];
      if (hours) return new Date(Date.now() + hours * 3600e3).toISOString();
      if (pauseFor === 'date') return pauseUntil ? pauseUntil.toDate().toISOString() : null;
      return null;
    }
    // A Send rule keeps an older snooze until it ends or is resumed.
    return d.until && !isEnded(d) ? d.until : null;
  };

  // The rule as it would be saved, for the summary line and the checks.
  const preview = useMemo<Rule | null>(() => {
    if (!draft) return null;
    const mappings = isDaily
      ? [{ platform: 'email', channels: { emails: email ? [email] : [], exclusion_emails: excluded } }]
      : buildMappings(shownDestinations, destValues).mappings;
    const rule: Rule = { ...draft, mappings, until: untilFor(draft) };
    if (!named) rule.name = suggestedName(rule, accountName);
    return rule;
  }, [draft, destValues, email, excluded, pauseFor, pauseUntil, named, shownDestinations, accountName]);

  const others = rules.filter((r) => r.id !== draft?.id);
  const accountIds = accounts.map((a) => a.id);
  const issues = preview ? ruleIssues(preview, others, accountIds, accountName).filter((i) => i.kind === 'warning' || i.kind === 'critical') : [];

  // The draft outlives the request for one render after the page closes the editor.
  if (!request || !draft || !preview) return null;

  const update = (patch: Partial<Rule>) => {
    setDraft({ ...draft, ...patch });
    setError('');
  };
  const types = typesOf(draft);
  // The hourly summary can't leave anything out, so grouping and exceptions don't mix.
  const leavesSomethingOut = (draft.scope === 'all' && draft.except.length > 0) || draft.otherExceptions.length > 0;
  const ek = eventTypeOf(draft.types);
  const noAccounts = coversNoAccounts(draft.types);
  const otherDaily = rules.some((r) => r.id !== draft.id && r.types !== 'all' && r.types.includes('daily_recap'));

  const toggleType = (key: RuleTypeKey) => {
    const def = RULE_TYPE[key];
    let next: Rule['types'];
    if (draft.types === 'all' || def.alone || (draft.types.length === 1 && RULE_TYPE[draft.types[0]].alone)) {
      next = [key];
    } else if (draft.types.includes(key)) {
      next = draft.types.length > 1 ? draft.types.filter((t) => t !== key) : draft.types;
    } else {
      next = [...draft.types, key];
    }
    const nextEk = eventTypeOf(next);
    update({ types: next, events: nextEk && nextEk !== ek ? RULE_TYPE[nextEk].defaultEvents || null : nextEk ? draft.events : null });
  };

  const accountOptions = accounts
    .filter((a) => !(draft.types !== 'all' && draft.types.length === 1 && draft.types[0] === 'cloud' && a.provider === 'K8s'))
    .map((a) => ({ value: a.id, label: a.name, group: a.provider || 'Other' }));

  const severityOptions = [
    { value: '', label: 'Any' },
    { value: 'high', label: 'High only' },
    ...(draft.severity && draft.severity !== 'high'
      ? [{ value: draft.severity, label: `${draft.severity.charAt(0).toUpperCase()}${draft.severity.slice(1)} only` }]
      : []),
  ];

  const save = async () => {
    const rule = { ...preview };
    if (!noAccounts && rule.scope === 'some' && !rule.accounts.length) return setError('Pick at least one account, or choose all accounts.');
    if (ek && rule.events && !rule.events.length) return setError('Pick at least one message.');
    if (rule.action === 'send') {
      if (isDaily) {
        if (email && !isValidEmail(email)) return setError('Enter a valid email address.');
        if (!email && users.length && excluded.length >= users.length) return setError('Add an email address, or leave at least one user in.');
      } else {
        const { problem, mappings } = buildMappings(shownDestinations, destValues);
        if (problem) return setError(problem);
        if (!mappings.length) return setError('Pick at least one channel.');
      }
    } else if (pauseFor === 'date' && !pauseUntil) {
      return setError('Pick the date and time the pause ends.');
    }
    if (rule.action === 'drop' && rule.until && new Date(rule.until).getTime() <= Date.now())
      return setError('The end time has to be in the future.');
    if (rule.action === 'send' && rule.batch && onlyTroubleshooting(rule.types) && leavesSomethingOut) {
      return setError('Hourly grouping can’t leave accounts or alerts out. Clear “except”, or turn grouping off.');
    }
    if (!rule.name || !isValidRuleName(rule.name)) {
      return setError('Rule names start with a letter and use letters, digits, spaces, _ and -.');
    }
    if (others.some((r) => r.name.toLowerCase() === rule.name.toLowerCase())) return setError('A notification rule with this name already exists.');

    const input = toUpsertInput(rule);
    if (input.expires_at) input.expires_at = formatter.formatDateWithTimezone(new Date(input.expires_at));
    setSaving(true);
    const res = await apiNotifications.upsertRule(input);
    setSaving(false);
    if (res.error) {
      setError(res.error);
      return;
    }
    toast.success(request.isNew ? 'Rule Created Successfully' : 'Rule Updated Successfully');
    onSaved();
  };

  const typeChip = (key: RuleTypeKey | 'all') => {
    const pressed = key === 'all' ? draft.types === 'all' : draft.types !== 'all' && draft.types.includes(key);
    const disabled = key === 'daily_recap' && otherDaily && !pressed;
    return (
      <Chip
        key={key}
        id={`rule-type-${key}`}
        variant='filter'
        size='sm'
        pressed={pressed}
        disabled={disabled}
        icon={key === 'all' ? undefined : <SafeIcon src={RULE_TYPE[key].icon} alt='' width={12} height={12} />}
        onClick={() => (key === 'all' ? update({ types: draft.types === 'all' ? ['troubleshoot'] : 'all', events: null }) : toggleType(key))}
      >
        {key === 'all' ? 'All types' : RULE_TYPE[key].label}
      </Chip>
    );
  };

  const moreLabel = [...(workloadFilters ? ['namespace', 'application'] : []), ...(alertFilters ? ['alert', 'severity'] : []), 'description'];

  return (
    <Modal
      open={open}
      handleClose={onClose}
      title={request.isNew ? 'New rule' : 'Edit rule'}
      width='md'
      loader={saving}
      backdropClickClose={!saving}
      actionButtons={
        <Stack direction='row' alignItems='center' gap={ds.space[3]} sx={{ width: '100%' }}>
          <Typography id='rule-error' role='alert' sx={{ flex: 1, fontSize: ds.text.small, color: ds.red[600] }}>
            {error}
          </Typography>
          <Button id='cancel-rule' tone='secondary' onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button id='save-rule' onClick={save} disabled={saving}>
            Save rule
          </Button>
        </Stack>
      }
    >
      <Box sx={{ display: 'flex', flexDirection: 'column', gap: ds.space[4], padding: ds.space[1] }}>
        <Box
          id='rule-summary'
          sx={{
            display: 'flex',
            alignItems: 'flex-start',
            gap: ds.space[2],
            padding: `${ds.space[2]} ${ds.space[3]}`,
            borderRadius: ds.radius.lg,
            backgroundColor: ds.background[200],
            fontSize: ds.text.body,
            color: ds.gray[700],
          }}
        >
          <RuleStatusLabel rule={preview} />
          <RuleSentence rule={preview} accountName={accountName} />
        </Box>
        {issues.map((issue) => (
          <Callout key={issue.text} tone={issue.kind === 'critical' ? 'critical' : 'warning'} message={issue.text} />
        ))}
        {!draft.active && (
          <Callout
            tone='info'
            message='This rule is off. It was limited to accounts before optimization rules could be. Turn it on from the rule’s menu.'
          />
        )}

        <Input
          id='notificationName'
          label='Name'
          instructionText='Optional. Suggested from the rule.'
          value={named ? draft.name : ''}
          placeholder={suggestedName(preview, accountName)}
          onChange={(value: string) => {
            setNamed(!!value);
            update({ name: value });
          }}
        />

        <Section title='Which'>
          <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: ds.space[1] }}>
            {typeChip('all')}
            {RULE_TYPES.map((t) => typeChip(t.key))}
          </Box>
          {ek && (
            <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: ds.space[1] }}>
              {RULE_TYPE[ek].events!.map((ev) => {
                const selected = draft.events || RULE_TYPE[ek].defaultEvents || [];
                return (
                  <Checkbox
                    key={ev.id}
                    id={`event-${ev.id}`}
                    size='sm'
                    label={ev.label}
                    description={ev.description}
                    checked={selected.includes(ev.id)}
                    onChange={(checked) => update({ events: checked ? [...selected, ev.id] : selected.filter((e) => e !== ev.id) })}
                  />
                );
              })}
            </Box>
          )}
          {ek === 'automation' && <Hint>Approval requests always go out.</Hint>}
          {draft.types === 'all' && <Hint>Every type, including ones added later, except the daily email. Approval requests always go out.</Hint>}
          {draft.types !== 'all' && types.some((t) => RULE_TYPE[t].rollup) && (
            <Hint>Digests covering several accounts are split: each channel only gets the accounts its rules allow.</Hint>
          )}
          <Box>
            <Button id='rule-more-options' tone='link' size='sm' onClick={() => setMoreOpen(!moreOpen)} aria-expanded={moreOpen}>
              {moreOpen ? 'Fewer options' : `More options (${listText(moreLabel)})`}
            </Button>
          </Box>
          {moreOpen && (
            <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: ds.space[3] }}>
              {workloadFilters && (
                <>
                  <Select
                    id='rule-namespace'
                    label='Namespace'
                    size='sm'
                    placeholder='Any'
                    clearable
                    value={draft.namespace || null}
                    options={
                      draft.namespace && !namespaces.some((n) => n.value === draft.namespace)
                        ? [{ value: draft.namespace, label: draft.namespace }, ...namespaces]
                        : namespaces
                    }
                    onChange={(value) => update({ namespace: value || '', workload: '' })}
                  />
                  <Select
                    id='rule-application'
                    label='Application'
                    size='sm'
                    placeholder='Any'
                    clearable
                    disabled={!draft.namespace}
                    value={draft.workload || null}
                    options={
                      draft.workload && !workloads.some((w) => w.value === draft.workload)
                        ? [{ value: draft.workload, label: draft.workload }, ...workloads]
                        : workloads
                    }
                    onChange={(value) => update({ workload: value || '' })}
                  />
                </>
              )}
              {alertFilters && (
                <>
                  <Select
                    id='rule-alert'
                    label='Alert'
                    size='sm'
                    placeholder='Any'
                    clearable
                    searchable
                    onCreateOption={(value) => {
                      update({ alert: value });
                      return true;
                    }}
                    value={draft.alert || null}
                    options={
                      draft.alert && !alerts.some((a) => a.value === draft.alert) ? [{ value: draft.alert, label: draft.alert }, ...alerts] : alerts
                    }
                    onChange={(value) => update({ alert: value || '' })}
                  />
                  <Select
                    id='rule-severity'
                    clearable={false}
                    label='Severity'
                    size='sm'
                    value={draft.severity}
                    options={severityOptions}
                    onChange={(value) => update({ severity: value })}
                  />
                </>
              )}
              <Box sx={{ gridColumn: '1 / -1' }}>
                <Input
                  id='rule-description'
                  label='Description'
                  size='sm'
                  value={draft.description}
                  onChange={(value: string) => update({ description: value })}
                />
              </Box>
            </Box>
          )}
        </Section>

        <Section title='From'>
          {noAccounts ? (
            <Hint>{`${listText(types.map((t) => RULE_TYPE[t].noun)).replace(/^./, (c) => c.toUpperCase())} always cover every account.`}</Hint>
          ) : (
            <>
              <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: '180px 1fr' }, gap: ds.space[2], alignItems: 'start' }}>
                <Select
                  id='rule-scope'
                  clearable={false}
                  size='sm'
                  value={draft.scope}
                  options={[
                    { value: 'all', label: 'All accounts' },
                    { value: 'some', label: 'These accounts' },
                  ]}
                  onChange={(value) => update({ scope: value as Rule['scope'] })}
                />
                {draft.scope === 'some' ? (
                  <Select
                    id='account-1'
                    multiple
                    size='sm'
                    grouped
                    placeholder='Choose accounts'
                    value={draft.accounts}
                    options={accountOptions}
                    onChange={(value) => update({ accounts: value })}
                  />
                ) : (
                  <Select
                    id='rule-except-accounts'
                    multiple
                    size='sm'
                    grouped
                    placeholder='Except: none'
                    value={draft.except}
                    options={accountOptions}
                    onChange={(value) => update({ except: value })}
                  />
                )}
              </Box>
              {draft.scope === 'some' && types.some((t) => !RULE_TYPE[t].accounts) && draft.types !== 'all' && (
                <Hint>
                  Doesn’t apply to {listText(types.filter((t) => !RULE_TYPE[t].accounts).map((t) => RULE_TYPE[t].noun))}: they always cover every
                  account.
                </Hint>
              )}
            </>
          )}
          {draft.otherExceptions.length > 0 && (
            <Hint>This rule also leaves out some namespaces, applications or alerts. They’re kept as they are.</Hint>
          )}
        </Section>

        <Section title='Do'>
          <Box>
            <ToggleGroup
              id='rule-action'
              selection='single'
              ariaLabel='What to do'
              value={draft.action}
              options={[
                { value: 'send', label: 'Send them' },
                { value: 'drop', label: 'Don’t send them' },
              ]}
              onChange={(value) => update({ action: value as Rule['action'] })}
            />
          </Box>
          {draft.action === 'send' ? (
            <>
              {isPaused(draft) && (
                <Callout
                  tone='warning'
                  message={`Not sent until ${formatWhen(draft.until!)}. After that it sends as below.`}
                  action={{ label: 'Resume now', id: 'rule-resume', onClick: () => update({ until: null }) }}
                />
              )}
              {isDaily ? (
                <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: ds.space[3] }}>
                  <Input
                    id='email-input'
                    label='Also send to'
                    size='sm'
                    placeholder='name@example.com'
                    value={email}
                    onChange={(value: string) => {
                      setEmail(value.trim());
                      setError('');
                    }}
                  />
                  <Select
                    id='exclude-users'
                    label='Leave out these users'
                    multiple
                    size='sm'
                    placeholder='None'
                    value={excluded}
                    options={users}
                    onChange={(value) => setExcluded(value)}
                  />
                </Box>
              ) : (
                <DestinationFields
                  idPrefix='rule'
                  ids={EDITOR_PICKER_IDS}
                  destinations={shownDestinations}
                  value={destValues}
                  onChange={setDestValues}
                />
              )}
              {onlyTroubleshooting(draft.types) && (
                <Checkbox
                  id='rule-batch'
                  size='sm'
                  label='Group into one message per hour'
                  description={
                    leavesSomethingOut && !draft.batch
                      ? 'Not available while the rule leaves accounts or alerts out'
                      : 'Slack only, posted to Slack’s default channel'
                  }
                  checked={draft.batch}
                  disabled={leavesSomethingOut && !draft.batch}
                  onChange={(checked) => update({ batch: checked })}
                />
              )}
            </>
          ) : (
            <>
              <Box sx={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: ds.space[2] }}>
                <Hint>For</Hint>
                <Box sx={{ minWidth: 180 }}>
                  <Select
                    id='rule-pause-for'
                    clearable={false}
                    size='sm'
                    value={pauseFor}
                    options={[
                      { value: 'always', label: 'until I change it' },
                      { value: '1h', label: '1 hour' },
                      { value: '24h', label: '24 hours' },
                      { value: '7d', label: '7 days' },
                      { value: 'date', label: 'until a date…' },
                    ]}
                    onChange={(value) => {
                      setPauseFor(value as PauseFor);
                      setError('');
                    }}
                  />
                </Box>
                {pauseFor === 'date' && (
                  <Box sx={{ minWidth: 220 }}>
                    <CustomDateTimePicker
                      id='rule-pause-until'
                      size='sm'
                      value={pauseUntil}
                      minDateTime={dayjs()}
                      onChange={(value: dayjs.Dayjs | null) => setPauseUntil(value)}
                    />
                  </Box>
                )}
              </Box>
              <Hint>Don’t send wins over every other rule, on every channel including email.</Hint>
            </>
          )}
        </Section>
      </Box>
    </Modal>
  );
};

export default RuleEditorModal;
