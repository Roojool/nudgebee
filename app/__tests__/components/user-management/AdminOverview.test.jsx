import React from 'react';
import { render, screen } from '@testing-library/react';
import AdminOverview from '@components/user-management/AdminOverview';

const mockRouter = { query: {}, asPath: '/user-management#overview', replace: jest.fn(() => Promise.resolve()) };
jest.mock('next/router', () => ({ useRouter: () => mockRouter }));
jest.mock('@components/user-management/AdminOverviewCards', () => ({
  __esModule: true,
  default: () => <div data-testid='overview-cards' />,
}));
jest.mock('@components/user-management/AdminConnections', () => ({
  __esModule: true,
  default: () => <div data-testid='admin-connections' />,
}));

describe('AdminOverview', () => {
  beforeEach(() => {
    mockRouter.query = {};
    mockRouter.asPath = '/user-management#overview';
    mockRouter.replace.mockClear();
  });

  it('opens on the Overview cards by default', () => {
    render(<AdminOverview />);
    expect(screen.getByTestId('overview-cards')).toBeInTheDocument();
    expect(screen.queryByTestId('admin-connections')).not.toBeInTheDocument();
  });

  it('opens on Connections when linked with ?action=overview-connections', () => {
    mockRouter.query = { action: 'overview-connections' };
    mockRouter.asPath = '/user-management?action=overview-connections#overview';
    render(<AdminOverview />);
    expect(screen.getByTestId('admin-connections')).toBeInTheDocument();
    expect(mockRouter.replace).toHaveBeenCalledWith('/user-management#overview', undefined, { shallow: true });
  });
});
