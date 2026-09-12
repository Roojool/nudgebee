import React from 'react';
import { render, screen, fireEvent } from '@testing-library/react';
import RunningTasksBanner from '@components/llm/common/RunningTasksBanner';

jest.mock('@utils/colors');

describe('RunningTasksBanner', () => {
  const sampleTasks = [
    { id: 't1', label: 'Scanning pod logs for errors' },
    { id: 't2', label: 'Checking cluster resource metrics' },
  ];

  it('renders nothing when not processing or tasks is empty', () => {
    const { container: c1 } = render(<RunningTasksBanner tasks={sampleTasks} isProcessing={false} />);
    expect(c1.firstChild).toBeNull();

    const { container: c2 } = render(<RunningTasksBanner tasks={[]} isProcessing={true} />);
    expect(c2.firstChild).toBeNull();
  });

  it('renders task count and task items when processing', () => {
    render(<RunningTasksBanner tasks={sampleTasks} isProcessing={true} />);

    expect(screen.getByText('2 tasks running')).toBeInTheDocument();
    expect(screen.getByText('Scanning pod logs for errors')).toBeInTheDocument();
    expect(screen.getByText('Checking cluster resource metrics')).toBeInTheDocument();
  });

  it('toggles collapse on header click', () => {
    render(<RunningTasksBanner tasks={sampleTasks} isProcessing={true} />);

    fireEvent.click(screen.getByTestId('running-tasks-header'));
    expect(screen.queryByText('Scanning pod logs for errors')).not.toBeInTheDocument();

    fireEvent.click(screen.getByTestId('running-tasks-header'));
    expect(screen.getByText('Scanning pod logs for errors')).toBeInTheDocument();
  });

  it('triggers onStop when stop button is clicked', () => {
    const onStop = jest.fn();
    render(<RunningTasksBanner tasks={sampleTasks} isProcessing={true} allowStop={true} onStop={onStop} />);

    const stopBtn = screen.getByTestId('running-tasks-stop');
    expect(stopBtn).toBeInTheDocument();
    fireEvent.click(stopBtn);
    expect(onStop).toHaveBeenCalledTimes(1);
  });
});
