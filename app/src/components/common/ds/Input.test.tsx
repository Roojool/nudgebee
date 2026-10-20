import { render, screen } from '@testing-library/react';
import { Input } from './Input';

// Emotion inserts rules via CSSOM in jsdom; gather them to inspect selectors.
const injectedCss = () =>
  Array.from(document.querySelectorAll('style'))
    .flatMap((el) => Array.from(el.sheet?.cssRules ?? []).map((r) => r.cssText))
    .join('\n');

const autofillRules = (css: string) => css.match(/[^{}]*:-webkit-autofill[^{}]*\{[^}]*\}/g) ?? [];

describe('Input autofill styling', () => {
  it('keeps the design-system fill when the browser autofills the field', () => {
    render(<Input label='K8s Secret' value='pg-secret' onChange={() => {}} size='sm' />);
    expect(screen.getByLabelText('K8s Secret')).toHaveValue('pg-secret');

    const rules = autofillRules(injectedCss());
    const base = rules.find((r) => !r.includes(':focus'));
    expect(base).toMatch(/box-shadow:\s*inset 0 0 0 1000px var\(--ds-background-100\)/);
    expect(base).toMatch(/-webkit-text-fill-color:\s*var\(--ds-gray-700\)/);
  });

  it('keeps the focus ring on an autofilled field', () => {
    render(<Input label='Name' value='postgresql' onChange={() => {}} />);
    const focusRule = autofillRules(injectedCss()).find((r) => r.includes(':focus'));
    expect(focusRule).toMatch(/inset 0 0 0 1000px var\(--ds-background-100\),\s*0 0 0 3px var\(--ds-blue-100\)/);
  });

  it('uses the disabled fill for a disabled autofilled field', () => {
    render(<Input label='Name' value='postgresql' onChange={() => {}} disabled />);
    const rules = autofillRules(injectedCss());
    expect(rules.some((r) => /inset 0 0 0 1000px var\(--ds-background-200\)/.test(r) && /--ds-gray-500/.test(r))).toBe(true);
  });

  it('keeps the error focus ring on an autofilled field', () => {
    render(<Input label='Name' value='postgresql' onChange={() => {}} error='Required' />);
    const rules = autofillRules(injectedCss());
    expect(rules.some((r) => r.includes(':focus') && /0 0 0 3px var\(--ds-red-100\)/.test(r))).toBe(true);
  });
});
