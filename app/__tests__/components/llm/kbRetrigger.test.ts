import { isKbRetriggerDisabled } from '@components/llm/kbRetrigger';

describe('isKbRetriggerDisabled', () => {
  it('offers Retrigger for a ready user-created KB', () => {
    expect(isKbRetriggerDisabled({ kb_type: 'manual', status: 'active', enabled: true }, true)).toBe(false);
  });

  it('offers Retrigger for a user-created KB whose last load failed', () => {
    expect(isKbRetriggerDisabled({ kb_type: 'manual', status: 'error', enabled: true }, true)).toBe(false);
  });

  it('offers Retrigger for a ready integration KB', () => {
    expect(isKbRetriggerDisabled({ kb_type: 'integration', status: 'active', enabled: true }, true)).toBe(false);
  });

  it('treats a KB with no enabled flag as enabled', () => {
    expect(isKbRetriggerDisabled({ kb_type: 'manual', status: 'active' }, true)).toBe(false);
  });

  it('disables Retrigger while a load is running', () => {
    expect(isKbRetriggerDisabled({ kb_type: 'manual', status: 'processing', enabled: true }, true)).toBe(true);
  });

  it('disables Retrigger for an archived KB', () => {
    expect(isKbRetriggerDisabled({ kb_type: 'integration', status: 'archived', enabled: true }, true)).toBe(true);
  });

  it('disables Retrigger for a switched-off KB', () => {
    expect(isKbRetriggerDisabled({ kb_type: 'manual', status: 'active', enabled: false }, true)).toBe(true);
  });

  it('disables Retrigger without write access', () => {
    expect(isKbRetriggerDisabled({ kb_type: 'manual', status: 'active', enabled: true }, false)).toBe(true);
    expect(isKbRetriggerDisabled({ kb_type: 'integration', status: 'active', enabled: true }, false)).toBe(true);
  });
});
