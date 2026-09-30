import { render, screen } from '@testing-library/react';
import { OptionalHardening, RequiredChangeDetails } from '../RemediationPanel';

// A fix no command applies (a chart value, a node pool) is shown as the change to make, not as the
// event's code diff.
describe('RequiredChangeDetails', () => {
  it('names where the change is made, its target and the change', () => {
    render(
      <RequiredChangeDetails
        change={{ where: 'deployment_source', target: 'Helm release eval-k4 values', change: 'image tag 1.27-alpine-doesnotexist -> 1.27-alpine' }}
      />
    );
    expect(screen.getByText('Change required — Deployment source (chart or GitOps values)')).toBeInTheDocument();
    expect(screen.getByText('Helm release eval-k4 values')).toBeInTheDocument();
    expect(screen.getByText('image tag 1.27-alpine-doesnotexist -> 1.27-alpine')).toBeInTheDocument();
  });

  it('falls back to a generic heading for an unknown location', () => {
    render(<RequiredChangeDetails change={{ where: 'elsewhere', change: 'do the thing' }} />);
    expect(screen.getByText('Change required — Change required')).toBeInTheDocument();
  });
});

// Hardening is labelled as not required, so a recovered one-off does not read as needing a fix.
describe('OptionalHardening', () => {
  it('labels the items as not required and shows each one', () => {
    render(
      <OptionalHardening
        items={[
          { target: 'lambda_function.py', change: 'reject malformed batches', reason: 'a future bad batch fails cleanly' },
          { change: 'alert on duration at 80% of the timeout' },
        ]}
      />
    );
    expect(screen.getByTestId('remediation-optional-hardening')).toHaveTextContent('Optional hardening — not required to resolve this incident');
    expect(screen.getByText('reject malformed batches', { exact: false })).toBeInTheDocument();
    expect(screen.getByText('alert on duration at 80% of the timeout')).toBeInTheDocument();
  });
});
