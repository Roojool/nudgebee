import React from 'react';
import { render, screen, fireEvent } from '@testing-library/react';
import { Switch } from '@ui/Switch';

describe('Switch', () => {
  it('renders with a label', () => {
    render(<Switch checked={false} onChange={jest.fn()} label='Enable notifications' />);
    expect(screen.getByText('Enable notifications')).toBeInTheDocument();
  });

  it('renders unchecked state', () => {
    render(<Switch checked={false} onChange={jest.fn()} label='Toggle' />);
    expect(screen.getByRole('checkbox')).not.toBeChecked();
  });

  it('renders checked state', () => {
    render(<Switch checked={true} onChange={jest.fn()} label='Toggle' />);
    expect(screen.getByRole('checkbox')).toBeChecked();
  });

  it('calls onChange when toggled (passes event and checked=true)', () => {
    const onChange = jest.fn();
    render(<Switch checked={false} onChange={onChange} label='Toggle' />);
    fireEvent.click(screen.getByRole('checkbox'));
    expect(onChange).toHaveBeenCalledWith(expect.anything(), true);
  });

  it('renders as disabled when disabled=true', () => {
    render(<Switch checked={false} onChange={jest.fn()} label='Toggle' disabled />);
    expect(screen.getByRole('checkbox')).toBeDisabled();
  });

  it('renders with description', () => {
    render(<Switch checked={false} onChange={jest.fn()} label='Label' description='Manage alerts' />);
    expect(screen.getByText('Manage alerts')).toBeInTheDocument();
  });

  it('renders sm size', () => {
    render(<Switch checked={false} onChange={jest.fn()} label='Small' size='sm' />);
    expect(screen.getByText('Small')).toBeInTheDocument();
  });

  it('renders without label using aria-label', () => {
    render(<Switch checked={false} onChange={jest.fn()} aria-label='Toggle feature' />);
    expect(screen.getByRole('checkbox', { name: 'Toggle feature' })).toBeInTheDocument();
  });

  it('renders loading state without crashing', () => {
    render(<Switch checked={false} onChange={jest.fn()} label='Loading' loading />);
    expect(screen.getByText('Loading')).toBeInTheDocument();
  });

  it('applies custom id', () => {
    render(<Switch checked={false} onChange={jest.fn()} id='my-switch' label='Switch' />);
    expect(document.getElementById('my-switch')).toBeInTheDocument();
  });

  // e2e selectors key off data-testid; it must land on the switch root, as with MUI's Switch.
  it('forwards data-* attributes to the switch base, and the checkbox stays inside it', () => {
    const onChange = jest.fn();
    render(<Switch checked={false} onChange={onChange} data-testid='task-disable-switch' aria-label='Disable task' />);
    const root = screen.getByTestId('task-disable-switch');
    const input = screen.getByRole('checkbox', { name: 'Disable task' });
    expect(root).toContainElement(input);
    fireEvent.click(input);
    expect(onChange).toHaveBeenCalledTimes(1);
  });

  // The label is a real <label> for the input, so clicking the text flips the switch.
  it.each([
    ['label only', {}],
    ['label + description', { description: 'Shown under the label' }],
  ])('toggles when the label text is clicked (%s)', (_name, extra) => {
    const onChange = jest.fn();
    render(<Switch checked={false} onChange={onChange} label='Raw JSON' {...extra} />);
    fireEvent.click(screen.getByText('Raw JSON'));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('checkbox', { name: 'Raw JSON' })).toBeInTheDocument();
  });

  it('does not toggle from the label when disabled', () => {
    const onChange = jest.fn();
    render(<Switch checked={false} onChange={onChange} label='Raw JSON' disabled />);
    fireEvent.click(screen.getByText('Raw JSON'));
    expect(onChange).not.toHaveBeenCalled();
  });

  it('puts a caller id on the input and passes checked=true on a label click', () => {
    const onChange = jest.fn();
    render(<Switch checked={false} onChange={onChange} id='my-toggle' label='Raw JSON' />);
    expect(document.getElementById('my-toggle')?.tagName).toBe('INPUT');
    fireEvent.click(screen.getByText('Raw JSON'));
    expect(onChange).toHaveBeenCalledWith(expect.anything(), true);
  });

  // KubernetesEvents renders a static id in every expanded row. The label wraps its own switch
  // (no htmlFor), so a click on row B's text flips row B, not the first input with that id.
  it('toggles the switch whose label was clicked, even when two share a static id', () => {
    const first = jest.fn();
    const second = jest.fn();
    render(
      <>
        <Switch checked={false} onChange={first} id='showTrend' label='Show Trend A' />
        <Switch checked={false} onChange={second} id='showTrend' label='Show Trend B' />
      </>
    );
    fireEvent.click(screen.getByText('Show Trend B'));
    expect(second).toHaveBeenCalledTimes(1);
    expect(first).not.toHaveBeenCalled();
  });

  // Playwright's check()/isChecked() on the test id resolve the control through the enclosing label.
  it('keeps the test id inside the label that wraps the switch', () => {
    render(<Switch checked={false} onChange={jest.fn()} label='Raw JSON' data-testid='call-workflow-advanced-toggle' />);
    expect(screen.getByTestId('call-workflow-advanced-toggle').closest('label')).not.toBeNull();
  });

  it('names the input from a non-string label, without folding in the description', () => {
    render(<Switch checked={false} onChange={jest.fn()} label={<span>Raw JSON</span>} description='Edit inputs as JSON' />);
    expect(screen.getByRole('checkbox', { name: 'Raw JSON' })).toBeInTheDocument();
  });

  it('forwards only data-* attributes', () => {
    const untyped = { className: 'leaked-class', title: 'leaked-title' } as Record<string, string>;
    const { container } = render(<Switch checked={false} onChange={jest.fn()} data-testid='sw' {...untyped} />);
    expect(screen.getByTestId('sw')).toBeInTheDocument();
    expect(container.querySelector('.leaked-class')).toBeNull();
    expect(container.querySelector('[title="leaked-title"]')).toBeNull();
  });

  it('keeps data-* attributes on the spinner while loading', () => {
    render(<Switch checked={false} onChange={jest.fn()} loading data-testid='task-disable-switch' />);
    expect(screen.getByTestId('task-disable-switch')).toHaveAttribute('aria-busy', 'true');
  });
});
