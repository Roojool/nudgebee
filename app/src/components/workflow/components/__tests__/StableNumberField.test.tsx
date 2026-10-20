import React from 'react';
import { render, screen } from '@testing-library/react';
import { StableNumberField } from '../StableFormFields';

describe('StableNumberField description layout', () => {
  const description = 'Max parallel iterations (0 or less means sequential, 1 means one at a time, >1 means N in parallel).';

  it('renders the description beside the input, not inside the narrow input column', () => {
    render(
      <StableNumberField fieldName='concurrency' value={1} onChange={jest.fn()} label='Concurrency' isRequired={false} description={description} />
    );
    const desc = screen.getByTestId('number-field-concurrency-description');
    expect(desc).toHaveTextContent(description);
    // Same flex row as the label and the input wrapper
    const row = desc.parentElement as HTMLElement;
    expect(row).toContainElement(screen.getByText('Concurrency'));
    expect(row).toContainElement(screen.getByRole('spinbutton'));
    expect(desc).not.toContainElement(screen.getByRole('spinbutton'));
    expect(screen.getByRole('spinbutton').closest('div')?.contains(desc)).toBe(false);
  });

  it('hides the description while an error is shown', () => {
    render(
      <StableNumberField
        fieldName='concurrency'
        value={1}
        onChange={jest.fn()}
        label='Concurrency'
        isRequired={false}
        description={description}
        error='Too high'
      />
    );
    expect(screen.queryByTestId('number-field-concurrency-description')).not.toBeInTheDocument();
  });
});
