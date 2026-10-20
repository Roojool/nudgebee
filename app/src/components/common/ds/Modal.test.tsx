import { render, screen } from '@testing-library/react';
import { Modal } from './Modal';

// The standard footer hardcoded tone='primary' on Confirm, so every dialog using
// it rendered a blue "Delete" — while dialogs that hand-roll `actionButtons`
// correctly used tone='danger'. Same product, two signals for the same moment.
// confirmTone brings the standard footer to parity.
//
// Asserted on the tone prop rather than the rendered colour: Emotion hashes its
// class names, and jsdom is not a reliable witness for computed style.
jest.mock('@ui/Button', () => ({
  Button: ({ children, tone, onClick, id }: any) => (
    <button data-tone={tone} id={id} onClick={onClick}>
      {children}
    </button>
  ),
}));

const toneOf = (name: string) => screen.getByRole('button', { name }).getAttribute('data-tone');

const renderModal = (props: Record<string, unknown> = {}) =>
  render(
    <Modal open title='Delete rule' confirmText='Delete' onConfirm={jest.fn()} {...props}>
      <div>body</div>
    </Modal>
  );

describe('Modal — confirmTone', () => {
  it('defaults the standard footer confirm to the primary tone', () => {
    renderModal();
    expect(toneOf('Delete')).toBe('primary');
  });

  it('renders the confirm in the danger tone when asked', () => {
    renderModal({ confirmTone: 'danger' });
    expect(toneOf('Delete')).toBe('danger');
  });

  it('leaves Cancel alone — only the confirm carries the tone', () => {
    renderModal({ confirmTone: 'danger' });
    expect(toneOf('Cancel')).toBe('secondary');
  });
});
