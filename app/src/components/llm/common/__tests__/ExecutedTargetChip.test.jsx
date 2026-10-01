import React from 'react';
import { render, screen } from '@testing-library/react';
import ExecutedTargetChip, { parseExecutedTarget } from '../ExecutedTargetChip';

describe('executed target', () => {
  it('reads where a step ran from persisted JSON metadata', () => {
    const metadata = JSON.stringify({
      nb_executed_target: { provider: 'kubernetes', name: 'k8s-prod', account_id: 'prod-id' },
      planner_iteration: 2,
    });
    expect(parseExecutedTarget(metadata)).toEqual({ provider: 'kubernetes', name: 'k8s-prod', account_id: 'prod-id' });
  });

  it('ignores steps that ran on the conversation account or have bad metadata', () => {
    expect(parseExecutedTarget(null)).toBeNull();
    expect(parseExecutedTarget('{"planner_iteration":1}')).toBeNull();
    expect(parseExecutedTarget('not json')).toBeNull();
    expect(parseExecutedTarget({ nb_executed_target: { provider: 'aws' } })).toBeNull();
  });

  it('renders the target name only when there is one', () => {
    const { rerender } = render(<ExecutedTargetChip metadata={{ nb_executed_target: { provider: 'aws', name: 'aws-prod' } }} />);
    expect(screen.getByTestId('executed-target-chip')).toHaveTextContent('aws-prod');
    rerender(<ExecutedTargetChip metadata={null} />);
    expect(screen.queryByTestId('executed-target-chip')).toBeNull();
  });
});
