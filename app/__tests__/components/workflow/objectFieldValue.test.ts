import { normalizeStoredObjectValue, resolveObjectFieldValue } from '@components/workflow/utils/fieldTypeUtils';

describe('resolveObjectFieldValue', () => {
  it('leaves half-typed JSON alone for raw-text editors', () => {
    // Regression: coercing this to {} handed JsonEditor a value it never
    // emitted, which reseeded the box on every keystroke.
    expect(resolveObjectFieldValue('{"A', true)).toBe('{"A');
    expect(resolveObjectFieldValue('', true)).toBe('');
  });

  it('coerces unparseable strings to {} for object editors', () => {
    expect(resolveObjectFieldValue('{"A', false)).toEqual({});
    expect(resolveObjectFieldValue('', false)).toEqual({});
    expect(resolveObjectFieldValue('   ', false)).toEqual({});
  });

  it('parses valid JSON strings for object editors', () => {
    expect(resolveObjectFieldValue('{"A":"b"}', false)).toEqual({ A: 'b' });
  });

  it('passes template strings through', () => {
    expect(resolveObjectFieldValue('{{ Configs.headers }}', false)).toBe('{{ Configs.headers }}');
    expect(resolveObjectFieldValue('{% if x %}{}{% endif %}', false)).toBe('{% if x %}{}{% endif %}');
  });

  it('rejects arrays for object editors — an array is not a valid object-field value', () => {
    expect(resolveObjectFieldValue('[1,2]', false)).toEqual({});
    expect(resolveObjectFieldValue('[1,2]', true)).toBe('[1,2]');
  });

  it('passes non-string values through untouched', () => {
    const value = { A: 'b' };
    expect(resolveObjectFieldValue(value, false)).toBe(value);
    expect(resolveObjectFieldValue(undefined, false)).toBeUndefined();
  });
});

describe('normalizeStoredObjectValue', () => {
  it('turns a blank string into {}', () => {
    expect(normalizeStoredObjectValue('', true)).toEqual({ changed: true, value: {} });
    expect(normalizeStoredObjectValue('   ', false)).toEqual({ changed: true, value: {} });
  });

  it('parses a stored JSON object string', () => {
    expect(normalizeStoredObjectValue('{"A":"b"}', true)).toEqual({ changed: true, value: { A: 'b' } });
    expect(normalizeStoredObjectValue('{"A":"b"}', false)).toEqual({ changed: true, value: { A: 'b' } });
  });

  it('leaves in-progress text alone for raw-text editors', () => {
    // Regression: this used to be rewritten to {} and pushed to the parent,
    // destroying the value the user was typing.
    expect(normalizeStoredObjectValue('{"A', true)).toEqual({ changed: false, value: '{"A' });
    expect(normalizeStoredObjectValue('123', true)).toEqual({ changed: false, value: '123' });
  });

  it('still falls back to {} for editors that need an object', () => {
    expect(normalizeStoredObjectValue('{"A', false)).toEqual({ changed: true, value: {} });
    expect(normalizeStoredObjectValue('123', false)).toEqual({ changed: true, value: {} });
  });

  it('rejects arrays', () => {
    expect(normalizeStoredObjectValue('[1,2]', false)).toEqual({ changed: true, value: {} });
    expect(normalizeStoredObjectValue('[1,2]', true)).toEqual({ changed: false, value: '[1,2]' });
  });

  it('leaves template strings and non-strings alone', () => {
    expect(normalizeStoredObjectValue('{{ Configs.headers }}', false)).toEqual({ changed: false, value: '{{ Configs.headers }}' });
    const value = { A: 'b' };
    expect(normalizeStoredObjectValue(value, false)).toEqual({ changed: false, value });
    expect(normalizeStoredObjectValue(undefined, false)).toEqual({ changed: false, value: undefined });
  });
});
