import { act, renderHook, waitFor } from '@testing-library/react';
import { useOnboardingReadiness } from '@hooks/useOnboardingReadiness';

jest.mock('@api1/account', () => ({
  __esModule: true,
  default: {
    getAllAccount: jest.fn(),
    listProviderCapabilities: jest.fn(),
  },
}));
jest.mock('@api1/kubernetes1', () => ({
  __esModule: true,
  default: {
    listK8sAccAgentHealth: jest.fn(),
  },
}));
jest.mock('@api1/knowledge-base', () => ({
  __esModule: true,
  default: {
    getKnowledgeBases: jest.fn(),
  },
}));

import apiAccount from '@api1/account';
import apiKnowledgeBase from '@api1/knowledge-base';

const mockGetAllAccount = apiAccount.getAllAccount as jest.Mock;
const mockListProviderCapabilities = apiAccount.listProviderCapabilities as jest.Mock;
const mockGetKnowledgeBases = apiKnowledgeBase.getKnowledgeBases as jest.Mock;

// AWS, not K8s: keeps every account out of the agent-health lookup's own
// filter, so these tests don't also need to mock `listK8sAccAgentHealth`.
const awsAccount = (id: string) => ({ id, cloud_provider: 'AWS', account_name: id, status: 'active' });
const payload = (accounts: unknown[]) => ({ data: { all_accounts: accounts, integrations: [], messaging_platforms: [] } });

describe('useOnboardingReadiness', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    mockListProviderCapabilities.mockResolvedValue({ data: { data: { observability_list_provider_capabilities: [] } } });
  });

  // Known issue #24: a fully-failed lookup used to collapse to `false` (a
  // confirmed empty result), indistinguishable from a lookup that genuinely
  // found nothing. `refresh()`'s own resolved value carries the raw
  // `hasKnowledgeBase` the fix touches — `readiness.items` alone can't tell
  // these two cases apart, since `skillsEvaluator` renders both as `blank`.
  describe('hasKnowledgeBase — known issue #24', () => {
    it('is null when the lookup call itself fails, not false', async () => {
      mockGetAllAccount.mockResolvedValue(payload([awsAccount('acc-1')]));
      mockGetKnowledgeBases.mockRejectedValue(new Error('service unavailable'));
      const { result } = renderHook(() => useOnboardingReadiness());
      await waitFor(() => expect(result.current.loading).toBe(false));

      let input: Awaited<ReturnType<typeof result.current.refresh>>;
      await act(async () => {
        input = await result.current.refresh();
      });
      expect(input?.hasKnowledgeBase).toBeNull();
    });

    // The real failure shape: `getKnowledgeBases` has its own internal
    // try/catch and never actually rejects — it resolves normally with an
    // `errors` entry instead. The rejection test above alone can't catch a
    // regression here, since the real API can't produce what it mocks.
    it('is null when the lookup resolves with errors, not just when it rejects', async () => {
      mockGetAllAccount.mockResolvedValue(payload([awsAccount('acc-1')]));
      mockGetKnowledgeBases.mockResolvedValue({ data: [], errors: [{ message: 'Failed to fetch knowledge bases' }] });
      const { result } = renderHook(() => useOnboardingReadiness());
      await waitFor(() => expect(result.current.loading).toBe(false));

      let input: Awaited<ReturnType<typeof result.current.refresh>>;
      await act(async () => {
        input = await result.current.refresh();
      });
      expect(input?.hasKnowledgeBase).toBeNull();
    });

    it('is false when the lookup succeeds and finds none', async () => {
      mockGetAllAccount.mockResolvedValue(payload([awsAccount('acc-1')]));
      mockGetKnowledgeBases.mockResolvedValue({ data: [] });
      const { result } = renderHook(() => useOnboardingReadiness());
      await waitFor(() => expect(result.current.loading).toBe(false));

      let input: Awaited<ReturnType<typeof result.current.refresh>>;
      await act(async () => {
        input = await result.current.refresh();
      });
      expect(input?.hasKnowledgeBase).toBe(false);
    });

    it('is true when the lookup succeeds and finds at least one', async () => {
      mockGetAllAccount.mockResolvedValue(payload([awsAccount('acc-1')]));
      mockGetKnowledgeBases.mockResolvedValue({ data: [{ id: 'kb-1', name: 'Runbooks' }] });
      const { result } = renderHook(() => useOnboardingReadiness());
      await waitFor(() => expect(result.current.loading).toBe(false));

      let input: Awaited<ReturnType<typeof result.current.refresh>>;
      await act(async () => {
        input = await result.current.refresh();
      });
      expect(input?.hasKnowledgeBase).toBe(true);
    });
  });

  // Known issue #17: raised from 10 to 30. Locking in the new number so a
  // future change to it is a deliberate edit here, not a silent drift.
  describe('telemetry detection account cap — known issue #17', () => {
    it('asks at most 30 accounts, not more', async () => {
      const accounts = Array.from({ length: 35 }, (_, i) => awsAccount(`acc-${i}`));
      mockGetAllAccount.mockResolvedValue(payload(accounts));
      mockGetKnowledgeBases.mockResolvedValue({ data: [] });
      const { result } = renderHook(() => useOnboardingReadiness());
      await waitFor(() => expect(result.current.loading).toBe(false));

      expect(mockListProviderCapabilities).toHaveBeenCalledTimes(30);
    });

    it('asks every account when there are fewer than the cap', async () => {
      const accounts = Array.from({ length: 5 }, (_, i) => awsAccount(`acc-${i}`));
      mockGetAllAccount.mockResolvedValue(payload(accounts));
      mockGetKnowledgeBases.mockResolvedValue({ data: [] });
      const { result } = renderHook(() => useOnboardingReadiness());
      await waitFor(() => expect(result.current.loading).toBe(false));

      expect(mockListProviderCapabilities).toHaveBeenCalledTimes(5);
    });
  });
});
