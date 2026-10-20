import {
  buildFilterExpression,
  canonicalizeFilterExpression,
  mergeLegacyEventType,
  migrateLegacyAccountFilter,
  parseFilterExpression,
  structuredCoversFilter,
  structuredFieldDescription,
  structuredFieldLabel,
} from '../eventFilter';

const ACCOUNT_UUID = '4f1c2b7e-8a90-4c31-9f2d-1b6ac5e70d34';
const OTHER_UUID = '9a0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d';

describe('buildFilterExpression', () => {
  it("never writes an account clause — the account is the trigger's `accounts` param", () => {
    expect(buildFilterExpression({ cluster: [ACCOUNT_UUID], event_type: ['KubePodCrashLooping'] })).toBe(
      '{{ event.event_type == "KubePodCrashLooping" }}'
    );
  });

  it('keeps the namespace filter on subject_namespace regardless of provider', () => {
    expect(buildFilterExpression({ namespace: ['AmazonEC2'] })).toBe('{{ event.subject_namespace == "AmazonEC2" }}');
  });

  it('joins every set field and skips the empty ones', () => {
    expect(buildFilterExpression({ event_type: ['KubePodCrashLooping'], namespace: ['payments'], source: [], priority: ['HIGH'] })).toBe(
      '{{ event.event_type == "KubePodCrashLooping" and event.subject_namespace == "payments" and event.priority == "HIGH" }}'
    );
  });

  it('matches any of several picked values with one in-list', () => {
    expect(buildFilterExpression({ priority: ['HIGH', 'MEDIUM'], namespace: ['payments'] })).toBe(
      '{{ event.subject_namespace == "payments" and event.priority in ["HIGH", "MEDIUM"] }}'
    );
  });

  it('escapes quotes and backslashes in a value, and reads them back unchanged', () => {
    const expr = buildFilterExpression({ event_type: ['"SysctlChanged" Condition Failed'], source: ['a\\b', 'c'] });
    expect(expr).toBe('{{ event.event_type == "\\"SysctlChanged\\" Condition Failed" and event.source in ["a\\\\b", "c"] }}');
    expect(parseFilterExpression(expr)).toEqual({ event_type: ['"SysctlChanged" Condition Failed'], source: ['a\\b', 'c'] });
    expect(structuredCoversFilter(expr, parseFilterExpression(expr))).toBe(true);
  });

  it('reads a single-quoted equality', () => {
    expect(parseFilterExpression("{{ event.source == 'integration-test' }}")).toEqual({ source: ['integration-test'] });
  });

  it('returns an empty string when nothing is set', () => {
    expect(buildFilterExpression({})).toBe('');
    expect(buildFilterExpression({ event_type: ['', ''] })).toBe('');
  });
});

describe('canonicalizeFilterExpression', () => {
  it('rewrites a legacy uuid-valued event.cluster onto event.cloud_account_id', () => {
    expect(canonicalizeFilterExpression(`{{ event.cluster == "${ACCOUNT_UUID}" }}`)).toBe(`{{ event.cloud_account_id == "${ACCOUNT_UUID}" }}`);
  });

  it('leaves a name-valued event.cluster alone — hand-authored filters match on the name', () => {
    const filter = '{{ event.cluster == "prod-us-east-1" }}';
    expect(canonicalizeFilterExpression(filter)).toBe(filter);
  });

  it('tolerates an empty filter', () => {
    expect(canonicalizeFilterExpression('')).toBe('');
  });
});

describe('parseFilterExpression', () => {
  it('round-trips what buildFilterExpression produced', () => {
    const values = { event_type: ['KubePodCrashLooping', 'KubeNodeNotReady'], namespace: ['payments'], source: ['prometheus'], priority: ['HIGH'] };
    expect(parseFilterExpression(buildFilterExpression(values))).toEqual(values);
  });

  it('reads a single-quoted in-list, the form the server writes', () => {
    expect(parseFilterExpression("{{ event.priority in ['HIGH', 'LOW'] }}")).toEqual({ priority: ['HIGH', 'LOW'] });
  });

  it('does not read an account clause as a structured field', () => {
    expect(parseFilterExpression(`{{ event.cloud_account_id == "${ACCOUNT_UUID}" and event.subject_namespace == "payments" }}`)).toEqual({
      namespace: ['payments'],
    });
  });

  it('does not adopt a name-valued event.cluster, so the sidebar keeps it in advanced mode', () => {
    expect(parseFilterExpression('{{ event.cluster == "prod-us-east-1" }}')).toEqual({});
  });

  it('returns nothing for a non-template filter', () => {
    expect(parseFilterExpression('event.cluster == "prod"')).toEqual({});
  });
});

describe('structuredFieldLabel / structuredFieldDescription', () => {
  it('labels the overloaded fields for cloud accounts', () => {
    expect(structuredFieldLabel('namespace', 'AWS')).toBe('Service');
    expect(structuredFieldDescription('namespace', 'AWS')).toBe('Cloud service (e.g. AmazonEC2, AWS_RDS)');
  });

  it('keeps the kubernetes wording for K8s accounts and when the provider is unknown', () => {
    expect(structuredFieldLabel('namespace', 'K8s')).toBe('Namespace');
    expect(structuredFieldLabel('namespace', '')).toBe('Namespace');
    expect(structuredFieldDescription('namespace', undefined)).toBe('Kubernetes namespace');
  });

  it('falls back to the static label for fields that mean the same thing everywhere', () => {
    expect(structuredFieldLabel('source', 'AWS')).toBe('Source');
    expect(structuredFieldLabel('priority', 'AWS')).toBe('Priority');
    expect(structuredFieldDescription('source', 'AWS')).toBe('');
  });
});

describe('migrateLegacyAccountFilter', () => {
  // ACCOUNT_UUID is the workflow's own account throughout: with nothing saved, a trigger listened
  // only to it, so a filter could only ever match its events.
  const OWN = ACCOUNT_UUID;

  it('moves a pinned own account into accounts and keeps the rest of the filter', () => {
    expect(
      migrateLegacyAccountFilter(
        `{{ event.event_type == "KubePodCrashLooping" and event.cloud_account_id == "${OWN}" and event.priority == "HIGH" }}`,
        [],
        OWN
      )
    ).toEqual({ filter: '{{ event.event_type == "KubePodCrashLooping" and event.priority == "HIGH" }}', accounts: [OWN], migrated: true });
  });

  it('empties the filter when the account was its only clause', () => {
    expect(migrateLegacyAccountFilter(`{{ event.cloud_account_id == "${OWN}" }}`, [], OWN)).toEqual({
      filter: '',
      accounts: [OWN],
      migrated: true,
    });
  });

  it('migrates the older uuid-valued event.cluster too', () => {
    expect(migrateLegacyAccountFilter(`{{ event.cluster == "${OWN}" and event.source == "prometheus" }}`, [], OWN)).toEqual({
      filter: '{{ event.source == "prometheus" }}',
      accounts: [OWN],
      migrated: true,
    });
  });

  it('keeps only the own account from an or-group, as only its events ever reached the trigger', () => {
    expect(
      migrateLegacyAccountFilter(
        `{{ (event.cloud_account_id == "${OWN}" or event.cloud_account_id == "${OTHER_UUID}") and event.priority == "HIGH" }}`,
        [],
        OWN
      )
    ).toEqual({ filter: '{{ event.priority == "HIGH" }}', accounts: [OWN], migrated: true });
  });

  it('takes every listed account from an or-group when the saved accounts include them', () => {
    expect(
      migrateLegacyAccountFilter(
        `{{ (event.cloud_account_id == "${OWN}" or event.cloud_account_id == "${OTHER_UUID}") and event.priority == "HIGH" }}`,
        [OWN, OTHER_UUID],
        OWN
      )
    ).toEqual({ filter: '{{ event.priority == "HIGH" }}', accounts: [OWN, OTHER_UUID], migrated: true });
  });

  it('keeps the overlap with accounts already saved on the trigger', () => {
    expect(migrateLegacyAccountFilter(`{{ event.cloud_account_id == "${OWN}" }}`, [OWN, OTHER_UUID], OWN)).toEqual({
      filter: '',
      accounts: [OWN],
      migrated: true,
    });
  });

  it('does not split on an "and" inside a quoted value', () => {
    expect(migrateLegacyAccountFilter(`{{ event.title == "cpu and memory" and event.cloud_account_id == "${OWN}" }}`, [], OWN)).toEqual({
      filter: '{{ event.title == "cpu and memory" }}',
      accounts: [OWN],
      migrated: true,
    });
  });

  it.each([
    ['an account clause or-ed with another condition', `{{ event.cloud_account_id == "${OWN}" or event.priority == "HIGH" }}`, []],
    ['separately parenthesised clauses joined by or', `{{ (event.cloud_account_id == "${OWN}") or (event.priority == "HIGH") }}`, []],
    ['two separate account clauses', `{{ event.cloud_account_id == "${OWN}" and event.cloud_account_id == "${OTHER_UUID}" }}`, []],
    ['an account that contradicts the saved accounts', `{{ event.cloud_account_id == "${OWN}" }}`, [OTHER_UUID]],
    // It never fired: only own-account events reached it. Converting would start it on OTHER.
    ['another account pinned with nothing saved', `{{ event.cloud_account_id == "${OTHER_UUID}" and event.priority == "HIGH" }}`, []],
    ['a name-valued event.cluster', '{{ event.cluster == "prod-us-east-1" }}', []],
    ['a filter with no account clause', '{{ event.priority == "HIGH" }}', []],
    ['a non-template filter', `event.cloud_account_id == "${OWN}"`, []],
    ['an empty filter', '', []],
  ])('leaves %s unchanged', (_label, filter, accounts) => {
    expect(migrateLegacyAccountFilter(filter, accounts, OWN)).toEqual({ filter, accounts, migrated: false });
  });

  it('leaves a pinned account unchanged when the own account is unknown and nothing is saved', () => {
    const filter = `{{ event.cloud_account_id == "${OWN}" }}`;
    expect(migrateLegacyAccountFilter(filter)).toEqual({ filter, accounts: [], migrated: false });
  });
});

describe('structuredCoversFilter', () => {
  it('treats a single-quoted server list as covered by the pickers', () => {
    const filter = "{{ event.priority in ['HIGH', 'LOW'] }}";
    expect(structuredCoversFilter(filter, parseFilterExpression(filter))).toBe(true);
  });

  it('is false when the expression says more than the pickers can', () => {
    const filter = '{{ event.priority == "HIGH" or event.source == "x" }}';
    expect(structuredCoversFilter(filter, parseFilterExpression(filter))).toBe(false);
  });
});

describe('mergeLegacyEventType', () => {
  it('folds a saved event_type into the filter', () => {
    expect(mergeLegacyEventType(['A', 'B'], '')).toBe('{{ event.event_type in ["A", "B"] }}');
    expect(mergeLegacyEventType('A', '{{ event.source == "x" }}')).toBe('{{ event.event_type == "A" and event.source == "x" }}');
  });

  it('brackets an old filter with a top-level or, so the type still gates every branch', () => {
    const old = "{{ event.event_type == 'X' or (event.labels.alertname | default('')) == 'X' }}";
    expect(mergeLegacyEventType('X', old)).toBe(
      "{{ event.event_type == \"X\" and (event.event_type == 'X' or (event.labels.alertname | default('')) == 'X') }}"
    );
  });
});
