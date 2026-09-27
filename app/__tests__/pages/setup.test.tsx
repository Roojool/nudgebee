import React from 'react';
import { render, screen } from '@testing-library/react';
import '@testing-library/jest-dom';
import SetupPage from '@pages/setup/index';

jest.mock('src/utils/colors');

// The card is covered in depth by its own suite. This asserts the route exists
// and mounts it — the thing that breaks if the page or its import is moved.
jest.mock('@components/onboarding/SetupCenter', () => ({
  __esModule: true,
  default: () => <div data-testid='setup-center-stub' />,
}));

describe('/setup', () => {
  it('mounts the Setup Center and nothing else', () => {
    render(<SetupPage />);
    expect(screen.getByTestId('setup-page')).toBeInTheDocument();
    expect(screen.getByTestId('setup-center-stub')).toBeInTheDocument();
  });
});
