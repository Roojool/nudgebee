import React from 'react';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import WorkflowTemplatesModal from '../WorkflowTemplatesModal';

jest.mock('@api1/workflow', () => ({
  __esModule: true,
  default: { listTemplates: jest.fn() },
}));

jest.mock('next/router', () => ({
  useRouter: () => ({ push: jest.fn(), query: {} }),
}));

import apiWorkflow from '@api1/workflow';

const mockList = (apiWorkflow as any).listTemplates as jest.Mock;

const template = (id: string, name: string, labels: string[]) => ({ id, name, description: '', category: 'monitoring', tags: { labels } });

const EC2 = template('1', 'EC2 Host Diagnostics', ['diagnostic']);
const RESTART = template('2', 'Rollout Restart', ['restart']);

const respond = (templates: any[]) => ({ data: { workflow_list_template: { templates } } });

const renderModal = () => render(<WorkflowTemplatesModal open onClose={jest.fn()} accountId='acct-1' />);

const typeSearch = (value: string) => fireEvent.change(screen.getByPlaceholderText('Search templates'), { target: { value } });

describe('WorkflowTemplatesModal search', () => {
  beforeEach(() => {
    mockList.mockReset();
  });

  it('sends the typed query to the server with the raised limit', async () => {
    mockList.mockImplementation((_category, name) => Promise.resolve(respond(name ? [EC2] : [EC2, RESTART])));

    renderModal();
    await screen.findByText('Rollout Restart');
    expect(mockList).toHaveBeenLastCalledWith(undefined, undefined, 200, undefined, undefined, undefined, undefined);

    typeSearch('  ec2 ');

    await waitFor(() => expect(mockList).toHaveBeenLastCalledWith(undefined, 'ec2', 200, undefined, undefined, undefined, undefined));
    await waitFor(() => expect(screen.queryByText('Rollout Restart')).not.toBeInTheDocument());
    expect(screen.getByText('EC2 Host Diagnostics')).toBeInTheDocument();
  });

  it('keeps the selected label chip while searching', async () => {
    mockList.mockImplementation((_category, name) => Promise.resolve(respond(name ? [EC2] : [EC2, RESTART])));

    renderModal();
    await screen.findByText('Rollout Restart');

    fireEvent.click(screen.getByRole('button', { name: 'restart' }));
    await waitFor(() => expect(screen.queryByText('EC2 Host Diagnostics')).not.toBeInTheDocument());

    typeSearch('ec2');

    // The search result has no 'restart' template: the chip stays selected and
    // still ANDs with the search, so nothing matches.
    await screen.findByText('No templates match "ec2"');
    expect(screen.getByRole('button', { name: 'restart' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('clears the search and label chips when the modal is closed and reopened', async () => {
    mockList.mockImplementation((_category, name) => Promise.resolve(respond(name ? [EC2] : [EC2, RESTART])));

    const { rerender } = renderModal();
    await screen.findByText('Rollout Restart');

    fireEvent.click(screen.getByRole('button', { name: 'restart' }));
    typeSearch('ec2');
    await waitFor(() => expect(mockList).toHaveBeenLastCalledWith(undefined, 'ec2', 200, undefined, undefined, undefined, undefined));

    rerender(<WorkflowTemplatesModal open={false} onClose={jest.fn()} accountId='acct-1' />);
    rerender(<WorkflowTemplatesModal open onClose={jest.fn()} accountId='acct-1' />);

    await screen.findByText('Rollout Restart');
    expect(screen.getByText('EC2 Host Diagnostics')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Search templates')).toHaveValue('');
    expect(screen.getByRole('button', { name: 'restart' })).toHaveAttribute('aria-pressed', 'false');
    expect(mockList).toHaveBeenLastCalledWith(undefined, undefined, 200, undefined, undefined, undefined, undefined);
  });

  it('shows a search-specific empty state', async () => {
    mockList.mockImplementation((_category, name) => Promise.resolve(respond(name ? [] : [EC2])));

    renderModal();
    await screen.findByText('EC2 Host Diagnostics');

    typeSearch('zzz');

    await screen.findByText('No templates match "zzz"');
  });
});
