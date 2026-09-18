import React from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Callout, type CalloutTone } from '@ui/Callout';

describe('Callout', () => {
  it('renders the message from either prop or children', () => {
    const { rerender } = render(<Callout message='From the prop' />);
    expect(screen.getByRole('status')).toHaveTextContent('From the prop');
    rerender(<Callout>From children</Callout>);
    expect(screen.getByRole('status')).toHaveTextContent('From children');
  });

  // Warning and critical interrupt a screen reader, a hint doesn't — the role carries that.
  it.each([
    ['info', 'status'],
    ['success', 'status'],
    ['warning', 'alert'],
    ['critical', 'alert'],
  ] as const)('renders tone %s as role %s', (tone, role) => {
    render(<Callout tone={tone} message='Hint' />);
    expect(screen.getByRole(role)).toHaveTextContent('Hint');
  });

  // role='alert' already implies assertive and role='status' polite. An explicit aria-live on
  // top either contradicts the role or makes some screen readers announce twice.
  it.each(['info', 'success', 'warning', 'critical'] as const)('sets no explicit aria-live for tone %s', (tone) => {
    const { container } = render(<Callout tone={tone} message='Hint' />);
    expect(container.firstElementChild).not.toHaveAttribute('aria-live');
  });

  // An untyped (.jsx) caller can pass any string, e.g. MUI Alert's 'error'. It falls back to
  // info (and warns in development).
  it.each(['error', 'toString', 'constructor'])('falls back to the info tone for unknown tone %s', (tone) => {
    const warn = jest.spyOn(console, 'warn').mockImplementation(() => {});
    render(<Callout tone={tone as unknown as CalloutTone} message='Still shown' />);
    expect(warn).toHaveBeenCalledWith(expect.stringContaining('Callout: unknown tone'));
    warn.mockRestore();
    expect(screen.getByRole('status')).toHaveTextContent('Still shown');
    // Specifically the info tone's icon, so a fallback to another tone would fail.
    expect(screen.getByTestId('InfoOutlinedIcon')).toBeInTheDocument();
  });

  it('passes data-testid and id through to the root', () => {
    render(<Callout id='mcp-hint' data-testid='mcp-integration-alert' message='Hint' />);
    const el = screen.getByTestId('mcp-integration-alert');
    expect(el).toHaveAttribute('id', 'mcp-hint');
  });

  // `role` is a compile error for typed callers, but `aria-live` (hyphenated) type-checks and an
  // untyped caller can pass either.
  // The tone must win so the accessibility semantics can't be swapped out.
  it('ignores a role or aria-live a caller passes', () => {
    const untyped = { role: 'log', 'aria-live': 'polite' } as Record<string, string>;
    render(<Callout tone='critical' message='Boom' {...untyped} />);
    const el = screen.getByRole('alert');
    expect(el).toHaveTextContent('Boom');
    expect(el).not.toHaveAttribute('aria-live');
    expect(screen.queryByRole('log')).toBeNull();
  });

  it('renders one action and calls it', async () => {
    const onClick = jest.fn();
    render(<Callout message='Hint' action={{ label: 'Manage integrations', onClick, id: 'manage-btn' }} />);
    const button = screen.getByRole('button', { name: 'Manage integrations' });
    expect(button).toHaveAttribute('id', 'manage-btn');
    await userEvent.click(button);
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it('renders no button when there is no action', () => {
    render(<Callout message='Hint' />);
    expect(screen.queryByRole('button')).toBeNull();
  });
});
