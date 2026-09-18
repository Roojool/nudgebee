import React from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Callout } from '@ui/Callout';

describe('Callout', () => {
  it('renders the message from either prop or children', () => {
    const { rerender } = render(<Callout message='From the prop' />);
    expect(screen.getByRole('status')).toHaveTextContent('From the prop');
    rerender(<Callout>From children</Callout>);
    expect(screen.getByRole('status')).toHaveTextContent('From children');
  });

  // Same split as Banner: warning and critical interrupt a screen reader, a hint doesn't.
  it.each([
    ['info', 'status'],
    ['success', 'status'],
    ['warning', 'alert'],
    ['critical', 'alert'],
  ] as const)('renders tone %s as role %s', (tone, role) => {
    render(<Callout tone={tone} message='Hint' />);
    expect(screen.getByRole(role)).toHaveTextContent('Hint');
  });

  // role='alert' implies assertive; an explicit aria-live must never contradict it.
  it.each([
    ['info', 'polite'],
    ['success', 'polite'],
    ['warning', 'assertive'],
    ['critical', 'assertive'],
  ] as const)('gives tone %s aria-live %s, matching its role', (tone, live) => {
    const { container } = render(<Callout tone={tone} message='Hint' />);
    expect(container.firstElementChild).toHaveAttribute('aria-live', live);
  });

  it('passes data-testid and id through to the root', () => {
    render(<Callout id='mcp-hint' data-testid='mcp-integration-alert' message='Hint' />);
    const el = screen.getByTestId('mcp-integration-alert');
    expect(el).toHaveAttribute('id', 'mcp-hint');
  });

  // The tone decides how a screen reader treats it, so a caller must not be able
  // to swap that out through the prop spread.
  it('keeps the tone-driven role and aria-live even when a caller passes its own', () => {
    render(<Callout tone='critical' role='log' aria-live='polite' message='Boom' />);
    const el = screen.getByRole('alert');
    expect(el).toHaveTextContent('Boom');
    expect(el).toHaveAttribute('aria-live', 'assertive');
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
