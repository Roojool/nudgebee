import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import InterpretationPanel from '../interpretation/InterpretationPanel';

describe('InterpretationPanel decision block', () => {
  it('renders the verdict, the readings that qualify it, and the impact as one block', () => {
    render(
      <InterpretationPanel
        verdict='Over-provisioned — reduce CPU and memory requests'
        confidence='High'
        safety={{ label: 'Plan it', tone: 'warning', help: 'Production callers are in the blast radius.' }}
        observedWindow='14 days · 1.25-min step'
        impact='Reclaims 344m vCPU & 3.2 Gi.'
      />
    );
    const block = screen.getByTestId('decision-block');
    expect(block).toHaveTextContent('Over-provisioned — reduce CPU and memory requests');
    expect(block).toHaveTextContent('High confidence');
    expect(screen.getByTestId('decision-safety')).toHaveTextContent('Plan it');
    expect(block).toHaveTextContent('14 days · 1.25-min step');
    expect(screen.getByTestId('decision-impact')).toHaveTextContent('Reclaims 344m vCPU & 3.2 Gi.');
  });

  // A recommendation with no resolvable title (rule name missing, no catalog
  // entry) still carries an impact line. The block must not vanish with the
  // headline; a fresh-context review found the narrowed guard dropping it.
  it('still shows the impact when there is no verdict to lead with', () => {
    render(<InterpretationPanel verdict='' impact='Security — reduces attack surface. No direct cost.' />);
    expect(screen.getByTestId('decision-impact')).toHaveTextContent('Security — reduces attack surface. No direct cost.');
  });

  it('renders nothing at all only when it has nothing to say', () => {
    const { container } = render(<InterpretationPanel verdict='' />);
    expect(container).toBeEmptyDOMElement();
  });

  it('keeps the risk slot when it is the only thing present', () => {
    render(<InterpretationPanel verdict='' risk='Reducing replicas lowers spare capacity.' />);
    expect(screen.getByText('Reducing replicas lowers spare capacity.')).toBeInTheDocument();
    expect(screen.queryByTestId('decision-block')).not.toBeInTheDocument();
  });
});
