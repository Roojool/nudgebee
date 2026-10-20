import React from 'react';
import { render, screen, fireEvent } from '@testing-library/react';
import { Banner, type BannerTone } from '@ui/Banner';

describe('Banner', () => {
  it('renders message text', () => {
    render(<Banner tone='info' message='Something to note' />);
    expect(screen.getByText('Something to note')).toBeInTheDocument();
  });

  it('renders title when provided', () => {
    render(<Banner tone='warning' title='Warning!' message='Check this out' />);
    expect(screen.getByText('Warning!')).toBeInTheDocument();
    expect(screen.getByText('Check this out')).toBeInTheDocument();
  });

  it('renders all tones', () => {
    const tones = ['info', 'success', 'warning', 'critical'] as const;
    tones.forEach((tone) => {
      const { unmount } = render(<Banner tone={tone} message={`${tone} message`} />);
      expect(screen.getByText(`${tone} message`)).toBeInTheDocument();
      unmount();
    });
  });

  // Shared guard with Callout: an unknown tone (e.g. MUI Alert's 'error' from a .jsx caller)
  // falls back to info instead of crashing on the palette lookup.
  it('falls back to the info tone for an unknown tone', () => {
    const warn = jest.spyOn(console, 'warn').mockImplementation(() => {});
    render(<Banner tone={'error' as unknown as BannerTone} message='Still shown' />);
    expect(screen.getByRole('status')).toHaveTextContent('Still shown');
    expect(screen.getByTestId('InfoOutlinedIcon')).toBeInTheDocument();
    expect(warn).toHaveBeenCalledWith(expect.stringContaining('Banner: unknown tone'));
    warn.mockRestore();
  });

  // warning used to be role='alert' with aria-live='polite' — a contradiction. The role alone
  // now carries the urgency (alert = assertive, status = polite), shared with ds/Callout.
  it.each([
    ['info', 'status'],
    ['success', 'status'],
    ['warning', 'alert'],
    ['critical', 'alert'],
  ] as const)('renders tone %s as role %s with no explicit aria-live', (tone, role) => {
    render(<Banner tone={tone} message='Msg' />);
    const el = screen.getByRole(role);
    expect(el).not.toHaveAttribute('aria-live');
  });

  it('renders dismiss button when dismissible=true', () => {
    const onDismiss = jest.fn();
    render(<Banner tone='info' message='Msg' dismissible onDismiss={onDismiss} />);
    const dismissBtn = screen.getByLabelText('Dismiss');
    expect(dismissBtn).toBeInTheDocument();
    fireEvent.click(dismissBtn);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it('does not render dismiss button when dismissible=false', () => {
    render(<Banner tone='info' message='Msg' />);
    expect(screen.queryByLabelText('Dismiss')).not.toBeInTheDocument();
  });

  it('renders action buttons', () => {
    const onClick = jest.fn();
    render(<Banner tone='warning' message='Msg' actions={[{ label: 'Fix it', onClick }]} />);
    fireEvent.click(screen.getByText('Fix it'));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it('renders section surface', () => {
    render(<Banner tone='info' message='Section msg' surface='section' />);
    expect(screen.getByText('Section msg')).toBeInTheDocument();
  });

  it('renders inline actions placement', () => {
    const onClick = jest.fn();
    render(<Banner tone='info' message='Inline' actionsPlacement='inline' actions={[{ label: 'Go', onClick }]} />);
    expect(screen.getByText('Go')).toBeInTheDocument();
  });
});
