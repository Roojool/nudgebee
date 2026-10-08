import {
  accountsSilencedByFilter,
  mergeFilterOptions,
  namespaceFieldCopy,
  silencedAccountsMessage,
  type FilterValuesByAccount,
} from '../eventFilterOptions';

const accounts = [
  { id: 'a', label: 'cubeapm poc' },
  { id: 'b', label: 'k8s-prod' },
];

const byAccount: FilterValuesByAccount = {
  a: { event_type: ['KubePodCrashLooping', 'NodeNotReady'], namespace: ['payments', 'default'], source: ['prometheus'] },
  b: { event_type: ['KubePodCrashLooping', 'PostgreSQLCacheHitRatio'], namespace: ['default'], source: ['prometheus'] },
};

describe('mergeFilterOptions', () => {
  it('lists every account’s values once, naming where a value is seen when not everywhere', () => {
    expect(mergeFilterOptions('event_type', accounts, byAccount, [])).toEqual([
      { value: 'KubePodCrashLooping', label: 'KubePodCrashLooping' },
      { value: 'NodeNotReady', label: 'NodeNotReady · cubeapm poc' },
      { value: 'PostgreSQLCacheHitRatio', label: 'PostgreSQLCacheHitRatio · k8s-prod' },
    ]);
  });

  it('does not tag values with a single account loaded', () => {
    expect(mergeFilterOptions('namespace', accounts, { a: byAccount.a }, [])).toEqual([
      { value: 'default', label: 'default' },
      { value: 'payments', label: 'payments' },
    ]);
  });

  it('keeps a picked value no account has seen, marked as such', () => {
    expect(mergeFilterOptions('source', accounts, byAccount, ['custom'])[0]).toEqual({ value: 'custom', label: 'custom · not seen' });
  });

  it('keeps picked values unmarked while nothing has loaded', () => {
    expect(mergeFilterOptions('source', accounts, {}, ['custom'])).toEqual([{ value: 'custom', label: 'custom' }]);
  });

  it('formats labels without changing values', () => {
    const [first] = mergeFilterOptions('event_type', accounts, byAccount, [], (v) => v.toLowerCase());
    expect(first).toEqual({ value: 'KubePodCrashLooping', label: 'kubepodcrashlooping' });
  });
});

describe('accountsSilencedByFilter', () => {
  it('names accounts where no picked value of a field has been seen', () => {
    expect(accountsSilencedByFilter({ namespace: ['payments'] }, accounts, byAccount)).toEqual([{ label: 'k8s-prod', fields: ['namespace'] }]);
  });

  it('is fine when at least one picked value per field is seen in each account', () => {
    expect(
      accountsSilencedByFilter({ event_type: ['NodeNotReady', 'PostgreSQLCacheHitRatio'], namespace: ['default'] }, accounts, byAccount)
    ).toEqual([]);
  });

  it('claims nothing about accounts that have not loaded', () => {
    expect(accountsSilencedByFilter({ namespace: ['payments'] }, accounts, { a: byAccount.a })).toEqual([]);
  });
});

describe('namespaceFieldCopy', () => {
  it('names the field after the providers in scope', () => {
    expect(namespaceFieldCopy(['K8s']).label).toBe('Namespace');
    expect(namespaceFieldCopy(['AWS']).label).toBe('Service');
    expect(namespaceFieldCopy(['K8s', 'AWS']).label).toBe('Namespace / Service');
  });
});

describe('mergeFilterOptions with many accounts', () => {
  it('counts accounts instead of naming them past two', () => {
    const many = ['a', 'b', 'c', 'd'].map((id) => ({ id, label: `acct-${id}` }));
    const values = (types: string[]) => ({ event_type: types, namespace: [], source: [] });
    const options = mergeFilterOptions('event_type', many, { a: values(['X', 'Y']), b: values(['X', 'Y']), c: values(['X']), d: values([]) }, []);
    expect(options).toEqual([
      { value: 'X', label: 'X · in 3 of 4 accounts' },
      { value: 'Y', label: 'Y · acct-a, acct-b' },
    ]);
  });
});

describe('silencedAccountsMessage', () => {
  const label = (t: string) => ({ event_type: 'Event Type', namespace: 'Namespace', source: 'Source' }[t] as string);

  it('names a few accounts and the field to blame', () => {
    expect(silencedAccountsMessage([{ label: 'aws-prod', fields: ['namespace'] }], 3, label)).toBe(
      "Events from aws-prod won't trigger this automation: none of the picked namespace values have been seen there."
    );
  });

  it('counts accounts past three', () => {
    const silenced = ['a', 'b', 'c', 'd', 'e'].map((l) => ({ label: l, fields: ['event_type' as const] }));
    expect(silencedAccountsMessage(silenced, 30, label)).toBe(
      "Events from 5 of 30 accounts (a, b, c and 2 more) won't trigger this automation: none of the picked event type values have been seen there."
    );
  });
});
