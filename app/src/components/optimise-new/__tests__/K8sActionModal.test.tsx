import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import K8sActionModal from '@components/optimise-new/K8sActionModal';
import recommendationApi from '@api1/recommendation';
import { toast } from '@ui/Toast';

jest.mock('@api1/recommendation', () => ({
  __esModule: true,
  default: { applyRecommendation: jest.fn() },
}));

jest.mock('@ui/Toast', () => ({
  __esModule: true,
  toast: { success: jest.fn(), error: jest.fn() },
}));

const applyRecommendation = recommendationApi.applyRecommendation as jest.Mock;

const banded = (band: string, changeClass: string, impact: Record<string, unknown>) => ({
  safety_band: band,
  finops_score_breakdown: { safety_band: band, change_class: changeClass, impact_summary: impact },
});

const volumeRec = {
  id: 'rec-pv',
  account_id: 'acct-1',
  category: 'RightSizing',
  rule_name: 'unused_pvc',
  recommendation: { metadata: { name: 'pvc-0a1b' }, spec: { capacity: { storage: '20Gi' }, claimRef: { namespace: 'payments', name: 'data-0' } } },
  ...banded('review', 'destructive', { dependent_count: 0, safety_reason: 'removal of a volume' }),
};

const resizeRec = {
  id: 'rec-resize',
  account_id: 'acct-1',
  category: 'RightSizing',
  rule_name: 'pv_rightsize',
  recommendation: {
    spec: { capacity: { storage: '100Gi' }, claimRef: { namespace: 'payments', name: 'data-ledger-0' } },
    recommendation: { recommend_size: 40 },
  },
  ...banded('risky', 'reductive', { dependent_count: 2, production_dependents: 2, safety_reason: '2 production dependents' }),
};

const confirmButton = (label: string) => screen.getByRole('button', { name: label });

beforeEach(() => {
  applyRecommendation.mockReset();
  (toast.success as jest.Mock).mockReset();
  (toast.error as jest.Mock).mockReset();
});

describe('K8sActionModal', () => {
  it('runs a reclaim only once the volume name is typed exactly, and reports the queued task', async () => {
    applyRecommendation.mockResolvedValue({ data: { id: 'res-1' } });
    const onSuccess = jest.fn();
    const onClose = jest.fn();
    render(<K8sActionModal open onClose={onClose} onSuccess={onSuccess} recommendation={volumeRec} agentConnected />);

    expect(screen.getByText('removal of a volume', { exact: false })).toBeInTheDocument();
    expect(confirmButton('Delete Volume')).toBeDisabled();

    fireEvent.change(screen.getByLabelText('Type pvc-0a1b to confirm'), { target: { value: 'pvc-0a1' } });
    expect(confirmButton('Delete Volume')).toBeDisabled();

    fireEvent.change(screen.getByLabelText('Type pvc-0a1b to confirm'), { target: { value: 'pvc-0a1b' } });
    expect(confirmButton('Delete Volume')).toBeEnabled();
    fireEvent.click(confirmButton('Delete Volume'));

    await waitFor(() => expect(applyRecommendation).toHaveBeenCalledWith('acct-1', 'rec-pv', { name: 'pvc-0a1b' }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(onClose).toHaveBeenCalled();
    expect(toast.success).toHaveBeenCalledWith(expect.stringContaining('pvc-0a1b'));
    // The typed name is the acknowledgement for a removal: no second step.
    expect(screen.queryByText('Ready to apply')).not.toBeInTheDocument();
  });

  it('surfaces the backend refusal instead of claiming success', async () => {
    applyRecommendation.mockResolvedValue({ errors: [{ message: 'agent task already pending' }] });
    const onSuccess = jest.fn();
    render(<K8sActionModal open onClose={jest.fn()} onSuccess={onSuccess} recommendation={volumeRec} agentConnected />);

    fireEvent.change(screen.getByLabelText('Type pvc-0a1b to confirm'), { target: { value: 'pvc-0a1b' } });
    fireEvent.click(confirmButton('Delete Volume'));

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('agent task already pending'));
    expect(onSuccess).not.toHaveBeenCalled();
  });

  it('keeps the action visible but unavailable while the agent is down', () => {
    render(<K8sActionModal open onClose={jest.fn()} recommendation={volumeRec} agentConnected={false} />);

    expect(screen.getByText('Agent not connected')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Type pvc-0a1b to confirm'), { target: { value: 'pvc-0a1b' } });
    expect(confirmButton('Delete Volume')).toBeDisabled();
    expect(applyRecommendation).not.toHaveBeenCalled();
  });

  it('asks for the one acknowledgement on a resize with production callers, then sends the chosen size', async () => {
    applyRecommendation.mockResolvedValue({ data: { id: 'res-2' } });
    render(<K8sActionModal open onClose={jest.fn()} recommendation={resizeRec} agentConnected />);

    const size = screen.getByLabelText('New size') as HTMLInputElement;
    expect(size.value).toBe('40');
    fireEvent.change(size, { target: { value: '50' } });
    fireEvent.click(confirmButton('Resize Volume'));

    // Nothing applied yet: the safety step is showing.
    expect(applyRecommendation).not.toHaveBeenCalled();
    expect(await screen.findByText('Ready to apply')).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole('button', { name: 'Resize Volume' }).at(-1)!);

    await waitFor(() =>
      expect(applyRecommendation).toHaveBeenCalledWith('acct-1', 'rec-resize', { name: 'data-ledger-0', namespace: 'payments', recommendedSize: 50 })
    );
  });

  it('refuses a size that is not above zero', () => {
    render(<K8sActionModal open onClose={jest.fn()} recommendation={resizeRec} agentConnected />);
    fireEvent.change(screen.getByLabelText('New size'), { target: { value: '0' } });
    expect(screen.getByText('Enter a size above zero')).toBeInTheDocument();
    expect(confirmButton('Resize Volume')).toBeDisabled();
  });
});
