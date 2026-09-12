import React from 'react';
import { render, screen, fireEvent } from '@testing-library/react';
import QueuedMessagesBanner from '@components/llm/common/QueuedMessagesBanner';

jest.mock('@utils/colors');

describe('QueuedMessagesBanner', () => {
  const defaultMessages = [
    {
      id: 'qm-1',
      text: 'close open PR 803 and 804',
      createdAt: 1000,
    },
    {
      id: 'qm-2',
      text: 'check deployment logs',
      images: [{ data: 'test', mime_type: 'image/png' }],
      createdAt: 2000,
    },
  ];

  it('shows edit progress and blocks competing queue actions until editing finishes', () => {
    const props = {
      queuedMessages: defaultMessages,
      isExpanded: true,
      onToggleExpanded: jest.fn(),
      onSendNow: jest.fn(),
      onEdit: jest.fn(),
      onDelete: jest.fn(),
    };
    const { rerender } = render(<QueuedMessagesBanner {...props} editingMessageId='qm-1' />);
    expect(screen.getByRole('status')).toHaveTextContent('Moving to composer…');
    for (const id of ['qm-1', 'qm-2']) {
      for (const action of ['edit', 'delete', 'send-now']) expect(screen.getByTestId(`queued-message-${action}-${id}`)).toBeDisabled();
    }
    fireEvent.click(screen.getByTestId('queued-message-edit-qm-2'));
    expect(props.onEdit).not.toHaveBeenCalled();
    rerender(<QueuedMessagesBanner {...props} />);
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
    expect(screen.getByTestId('queued-message-edit-qm-1')).toBeEnabled();
  });

  it('renders nothing when queuedMessages is empty', () => {
    const { container } = render(
      <QueuedMessagesBanner
        queuedMessages={[]}
        isExpanded={true}
        onToggleExpanded={jest.fn()}
        onSendNow={jest.fn()}
        onEdit={jest.fn()}
        onDelete={jest.fn()}
      />
    );
    expect(container.firstChild).toBeNull();
  });

  it('renders header with count and subtitle', () => {
    render(
      <QueuedMessagesBanner
        queuedMessages={defaultMessages}
        isExpanded={true}
        onToggleExpanded={jest.fn()}
        onSendNow={jest.fn()}
        onEdit={jest.fn()}
        onDelete={jest.fn()}
      />
    );

    expect(screen.getByText('2 waiting')).toBeInTheDocument();
    expect(screen.getByTestId('queued-messages-count')).toHaveTextContent('2');
    expect(screen.getByText('Runs after the current response')).toBeInTheDocument();
  });

  it('calls onToggleExpanded when header is clicked', () => {
    const onToggleExpanded = jest.fn();
    render(
      <QueuedMessagesBanner
        queuedMessages={defaultMessages}
        isExpanded={true}
        onToggleExpanded={onToggleExpanded}
        onSendNow={jest.fn()}
        onEdit={jest.fn()}
        onDelete={jest.fn()}
      />
    );

    fireEvent.click(screen.getByTestId('queued-messages-header'));
    expect(onToggleExpanded).toHaveBeenCalledTimes(1);
  });

  it('renders items when isExpanded is true', () => {
    render(
      <QueuedMessagesBanner
        queuedMessages={defaultMessages}
        isExpanded={true}
        onToggleExpanded={jest.fn()}
        onSendNow={jest.fn()}
        onEdit={jest.fn()}
        onDelete={jest.fn()}
      />
    );

    expect(screen.getByText('close open PR 803 and 804')).toBeInTheDocument();
    expect(screen.getByText('check deployment logs')).toBeInTheDocument();
  });

  it('does not render items when isExpanded is false', () => {
    render(
      <QueuedMessagesBanner
        queuedMessages={defaultMessages}
        isExpanded={false}
        onToggleExpanded={jest.fn()}
        onSendNow={jest.fn()}
        onEdit={jest.fn()}
        onDelete={jest.fn()}
      />
    );

    expect(screen.queryByText('close open PR 803 and 804')).not.toBeInTheDocument();
  });

  it('triggers onSendNow, onEdit, and onDelete with message id', () => {
    const onSendNow = jest.fn();
    const onEdit = jest.fn();
    const onDelete = jest.fn();

    render(
      <QueuedMessagesBanner
        queuedMessages={defaultMessages}
        isExpanded={true}
        onToggleExpanded={jest.fn()}
        onSendNow={onSendNow}
        onEdit={onEdit}
        onDelete={onDelete}
      />
    );

    fireEvent.click(screen.getByTestId('queued-message-send-now-qm-1'));
    expect(onSendNow).toHaveBeenCalledWith('qm-1');

    fireEvent.click(screen.getByTestId('queued-message-edit-qm-1'));
    expect(onEdit).toHaveBeenCalledWith('qm-1');

    fireEvent.click(screen.getByTestId('queued-message-delete-qm-2'));
    expect(onDelete).toHaveBeenCalledWith('qm-2');
  });

  it('renders author badge when user is a team member', () => {
    render(
      <QueuedMessagesBanner
        queuedMessages={[
          {
            id: 'qm-3',
            text: 'team member follow-up',
            user: 'Alice',
            createdAt: 3000,
          },
        ]}
        isExpanded={true}
        onToggleExpanded={jest.fn()}
        onSendNow={jest.fn()}
        onEdit={jest.fn()}
        onDelete={jest.fn()}
      />
    );

    expect(screen.getByText('Alice')).toBeInTheDocument();
    expect(screen.getByText('team member follow-up')).toBeInTheDocument();
  });

  it('renders capacity when maxQueuedMessages is provided', () => {
    render(
      <QueuedMessagesBanner
        queuedMessages={defaultMessages}
        isExpanded={true}
        maxQueuedMessages={5}
        onToggleExpanded={jest.fn()}
        onSendNow={jest.fn()}
        onEdit={jest.fn()}
        onDelete={jest.fn()}
      />
    );

    expect(screen.getByTestId('queued-messages-count')).toHaveTextContent('2/5');
    expect(screen.getByText('Runs after the current response')).toBeInTheDocument();
  });

  it('renders full capacity warning when queue is at or above limit', () => {
    render(
      <QueuedMessagesBanner
        queuedMessages={defaultMessages}
        isExpanded={true}
        maxQueuedMessages={2}
        onToggleExpanded={jest.fn()}
        onSendNow={jest.fn()}
        onEdit={jest.fn()}
        onDelete={jest.fn()}
      />
    );

    expect(screen.getByTestId('queued-messages-count')).toHaveTextContent('2/2');
    expect(screen.getByText(/Queue full/i)).toBeInTheDocument();
  });
});
