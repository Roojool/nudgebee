import { buildInitialSubTaskParams } from '../subtaskDefaults';

describe('buildInitialSubTaskParams', () => {
  it('commits non-empty schema defaults', () => {
    const def = {
      input_schema: {
        timeout: { type: 'string', default: '30s' },
        verbose: { type: 'boolean', default: false },
        note: { type: 'string', default: '' },
        name: { type: 'string' },
      },
    };
    expect(buildInitialSubTaskParams(def)).toEqual({ timeout: '30s', verbose: false });
  });

  it('picks the only option of a required choice (DM provider)', () => {
    const dm = {
      input_schema: {
        provider: { type: 'string', required: true, options: ['slack'] },
        user_id: { type: 'string', required: true },
        mode: { type: 'string', options: ['a'] },
        region: { type: 'string', required: true, enum: ['us', 'eu'] },
      },
    };
    expect(buildInitialSubTaskParams(dm)).toEqual({ provider: 'slack' });
  });

  it('wraps the only option of a required multi-select in a list', () => {
    const def = { input_schema: { channels: { type: 'array', required: true, options: ['email'] } } };
    expect(buildInitialSubTaskParams(def)).toEqual({ channels: ['email'] });
  });

  it('prefills account_id from the workflow account only when the schema has it', () => {
    const withAccount = { input_schema: { account_id: { type: 'account', required: true } } };
    expect(buildInitialSubTaskParams(withAccount, 'acct-1')).toEqual({ account_id: 'acct-1' });
    expect(buildInitialSubTaskParams({ input_schema: { query: { type: 'string' } } }, 'acct-1')).toEqual({});
    expect(buildInitialSubTaskParams(withAccount)).toEqual({});
  });

  it('handles a missing definition', () => {
    expect(buildInitialSubTaskParams(undefined, 'acct-1')).toEqual({});
  });
});
