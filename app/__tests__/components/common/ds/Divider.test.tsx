import React from 'react';
import { render, type RenderResult } from '@testing-library/react';
import createCache from '@emotion/cache';
import { CacheProvider } from '@emotion/react';
import { Divider } from '@ui/Divider';

describe('Divider', () => {
  it('renders a horizontal separator by default', () => {
    const { container } = render(<Divider />);
    const sep = container.querySelector('[role="separator"]');
    expect(sep).toBeInTheDocument();
  });

  it('renders a vertical separator with aria-orientation vertical', () => {
    const { container } = render(<Divider orientation='vertical' />);
    const sep = container.querySelector('[role="separator"]');
    expect(sep).toBeInTheDocument();
    expect(sep).toHaveAttribute('aria-orientation', 'vertical');
  });

  // The browser gives <hr> `margin-inline: auto`, which shrinks it to zero width
  // inside a flex column. The primitive must reset the side margins.
  // jsdom doesn't compute these margins, so read the Emotion rule for the element.
  // A fresh Emotion cache per render, so the rule is inserted into the live sheet
  // even when an earlier test already rendered the same styles.
  let cacheSeq = 0;
  const renderWithFreshStyles = (ui: React.ReactElement): RenderResult =>
    render(<CacheProvider value={createCache({ key: `divider${cacheSeq++}` })}>{ui}</CacheProvider>);
  const ruleStyleFor = (el: Element): CSSStyleDeclaration | undefined => {
    const classes = Array.from(el.classList).map((c) => `.${c}`);
    const rules = Array.from(document.styleSheets).flatMap((sheet) => Array.from(sheet.cssRules)) as CSSStyleRule[];
    const rule = rules.find((r) => r.selectorText && classes.includes(r.selectorText));
    return rule?.style;
  };

  it('resets the horizontal line side margins so it spans a flex column', () => {
    const { container } = renderWithFreshStyles(<Divider />);
    const style = ruleStyleFor(container.querySelector('hr') as HTMLElement);
    expect(style?.getPropertyValue('margin-left')).toBe('0px');
    expect(style?.getPropertyValue('margin-right')).toBe('0px');
    expect(style?.getPropertyValue('flex-shrink')).toBe('0');
  });

  it('still lets sx override the side margins', () => {
    const { container } = renderWithFreshStyles(<Divider sx={{ mx: 'auto' }} />);
    const style = ruleStyleFor(container.querySelector('hr') as HTMLElement);
    expect(style?.getPropertyValue('margin-left')).toBe('auto');
    expect(style?.getPropertyValue('margin-right')).toBe('auto');
  });
});
