import React from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import ExecutionStatusBar from '../ExecutionStatusBar';

describe('ExecutionStatusBar approval options', () => {
  it('exposes the full text of a long option on hover and still submits it', () => {
    const onApprove = jest.fn();
    const longOpt = 'subnet-0d5b5fb47a8b8c0 (192.168.32.0/19 - us-west-2c - eksctl-ticketai-cluster-cluster/SubnetPublicUSWEST2C)';
    render(<ExecutionStatusBar visible pendingApprovals={[{ taskId: 'select-subnet', options: ['Create New', longOpt] }]} onApprove={onApprove} />);

    const btn = screen.getByRole('button', { name: longOpt });
    expect(btn).toHaveAttribute('title', longOpt);
    fireEvent.click(btn);
    expect(onApprove).toHaveBeenCalledWith('select-subnet', longOpt, undefined);
  });
});
