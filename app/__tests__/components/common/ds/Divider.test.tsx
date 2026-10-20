import React from 'react';
import { render } from '@testing-library/react';
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
  // inside a flex column. jsdom doesn't compute these margins, so read the
  // Emotion rule for the element (CSSOM declarations only support getPropertyValue).
  const ruleStyleFor = (el: Element): CSSStyleDeclaration | undefined => {
    const classes = Array.from(el.classList).map((c) => `.${c}`);
    const rules = Array.from(document.styleSheets).flatMap((sheet) => Array.from(sheet.cssRules)) as CSSStyleRule[];
    const rule = rules.find((r) => r.selectorText && classes.includes(r.selectorText));
    return rule?.style;
  };

  it('resets the horizontal line side margins and stretches it across a flex column', () => {
    const { container } = render(<Divider />);
    const style = ruleStyleFor(container.querySelector('hr') as HTMLElement);
    expect(style?.getPropertyValue('margin-left')).toBe('0px');
    expect(style?.getPropertyValue('margin-right')).toBe('0px');
    expect(style?.getPropertyValue('align-self')).toBe('stretch');
    expect(style?.getPropertyValue('flex-shrink')).toBe('0');
  });

  it('stretches the labelled divider so its flanking lines keep their width in an aligned column', () => {
    const { container } = render(<Divider label='or' />);
    const style = ruleStyleFor(container.querySelector('[role="separator"]') as HTMLElement);
    expect(style?.getPropertyValue('align-self')).toBe('stretch');
  });

  it('still lets sx override the side margins and alignment', () => {
    const { container } = render(<Divider sx={{ mx: 'auto', alignSelf: 'flex-end' }} />);
    const style = ruleStyleFor(container.querySelector('hr') as HTMLElement);
    expect(style?.getPropertyValue('margin-left')).toBe('auto');
    expect(style?.getPropertyValue('margin-right')).toBe('auto');
    expect(style?.getPropertyValue('align-self')).toBe('flex-end');
  });
});
