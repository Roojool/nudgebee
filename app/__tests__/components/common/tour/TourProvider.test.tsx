/**
 * A tour started just before the Setup gate re-locks (SetupCenter's own
 * account-create callback can flip `locked` back to true mid-wizard — see
 * SetupGateContext's doc comment) must not keep running once it does. The
 * auto-trigger components (FirstLoginTour/SectionFirstVisitTour) stop
 * *offering* a new tour once locked, but nothing tore down one already in
 * flight before this — that's what this file pins.
 */
import * as React from 'react';
import { render, act } from '@testing-library/react';
import { TourProvider, useTour } from '@components/common/tour/TourProvider';
import { useSetupGate } from '@context/SetupGateContext';

const destroySpy = jest.fn();
const highlightSpy = jest.fn();

jest.mock('driver.js', () => ({
  driver: jest.fn((config: { onDestroyed?: () => void }) => ({
    destroy: (...args: unknown[]) => {
      destroySpy(...args);
      config.onDestroyed?.();
    },
    highlight: (...args: unknown[]) => highlightSpy(...args),
  })),
}));

jest.mock('@context/SetupGateContext', () => ({
  useSetupGate: jest.fn(),
}));
const mockUseSetupGate = useSetupGate as jest.Mock;

jest.mock('@components/common/tour/tours', () => ({
  TOURS: {
    'test-tour': {
      steps: [{ element: '#tour-target', title: 't', description: 'd' }],
    },
  },
  brandText: (s: string) => s,
}));

const gateValue = (locked: boolean) => ({ loading: false, locked, canManageSetup: true, refresh: jest.fn() });

function Harness() {
  const { start, isActive } = useTour();
  return (
    <div>
      {/* Matches the mocked tour's step selector so waitForElement resolves
          synchronously, instead of falling through to its real 5s timer. */}
      <div id='tour-target' />
      <button onClick={() => start('test-tour')}>start</button>
      <div data-testid='active'>{String(isActive)}</div>
    </div>
  );
}

describe('TourProvider — stop on Setup gate lock', () => {
  beforeEach(() => {
    destroySpy.mockClear();
    highlightSpy.mockClear();
    mockUseSetupGate.mockReturnValue(gateValue(false));
  });

  it('destroys an in-flight tour the instant the gate locks', async () => {
    const { getByText, getByTestId, rerender } = render(
      <TourProvider>
        <Harness />
      </TourProvider>
    );

    await act(async () => {
      getByText('start').click();
    });
    expect(getByTestId('active').textContent).toBe('true');
    expect(destroySpy).not.toHaveBeenCalled();

    mockUseSetupGate.mockReturnValue(gateValue(true));
    await act(async () => {
      rerender(
        <TourProvider>
          <Harness />
        </TourProvider>
      );
    });

    expect(destroySpy).toHaveBeenCalledTimes(1);
    expect(getByTestId('active').textContent).toBe('false');
  });

  it('does nothing when the gate locks with no tour active', async () => {
    render(
      <TourProvider>
        <Harness />
      </TourProvider>
    );

    mockUseSetupGate.mockReturnValue(gateValue(true));
    await act(async () => {
      // Locking alone, no start() — nothing to destroy.
    });

    expect(destroySpy).not.toHaveBeenCalled();
  });
});
