import { render, screen } from '@testing-library/react';

import FollowupHistory from '@components/llm/common/FollowupHistory';

const task = (followupType, question, text, status) => ({
  type: 'followup-question',
  tool: 'followup-question',
  asked_at: '2026-10-01T08:05:00Z',
  last_updated_at: '2026-10-01T08:12:00Z',
  response: { text, status, message_config: JSON.stringify({ followupType, toolName: 'kubectl_execute', question }) },
});

describe('FollowupHistory', () => {
  it('renders nothing when the turn asked nothing', () => {
    const { container } = render(<FollowupHistory followups={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows each question and approval with how it ended', () => {
    render(
      <FollowupHistory
        followups={[
          task('tool_config', 'Which k8s-prod Postgres integration?', 'prod', 'COMPLETED'),
          task(
            'tool_confirmation',
            'Tool(kubectl_execute) is trying to update cluster resources.\nCommand - {"command":"kubectl delete pod web-1"}',
            'yes',
            'COMPLETED'
          ),
          task('text', 'Which namespace?', '', 'WAITING'),
        ]}
      />
    );

    const items = screen.getAllByTestId('followup-history-item');
    expect(items.map((i) => i.dataset.outcome)).toEqual(['answered', 'approved', 'unanswered']);
    expect(screen.getByText('Which k8s-prod Postgres integration?')).toBeInTheDocument();
    expect(screen.getByText('prod')).toBeInTheDocument();
    expect(screen.getByText('Approval requested')).toBeInTheDocument();
    expect(screen.getByText('kubectl delete pod web-1')).toBeInTheDocument();
    expect(screen.getByText('Approved')).toBeInTheDocument();
    expect(screen.getByText('No answer')).toBeInTheDocument();
  });
});
