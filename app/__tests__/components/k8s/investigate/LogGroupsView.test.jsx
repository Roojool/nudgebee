import React from 'react';
import { render, screen, fireEvent } from '@testing-library/react';
import LogGroupsView, { RAW_LOG_LINE_LIMIT } from '@components/k8s/investigate/cards/LogGroupsView';

// The raw viewer is exercised elsewhere; stub it so these assertions are about
// which view the card picks.
jest.mock('@shared/viewers/ConsoleLogOutput', () => ({
  __esModule: true,
  default: ({ data }) => <pre data-testid='raw-logs'>{data}</pre>,
}));

// Shape of additional_info on a pod log evidence, as the api-server pod-log action
// stores it (eventrule/playbooks/log_summary.go).
const groupedInfo = {
  log_total_lines: 1000,
  log_groups: [
    {
      text: 'Error when appending sample from OTLP: out of order sample',
      count: 898,
      level: 'error',
      first_seen: '2026-09-17T00:03:04.296Z',
      last_seen: '2026-09-17T00:03:25.170Z',
      source: 'combined_appender.go:181',
    },
    { text: 'WAL checkpoint complete', count: 1, level: 'info', first_seen: '2026-09-17T01:00:30.518Z', last_seen: '2026-09-17T01:00:30.518Z' },
    { text: 'ready', count: 101, level: 'unknown' },
  ],
};

describe('LogGroupsView', () => {
  it('shows the groups, not the raw file, for a long log', () => {
    render(<LogGroupsView logs='raw text' additionalInfo={groupedInfo} />);

    expect(screen.getByText('1,000 lines, grouped by what they say')).toBeInTheDocument();
    expect(screen.getAllByTestId('log-groups-row')).toHaveLength(2);
    expect(screen.getByText('898')).toBeInTheDocument();
    expect(screen.getByText('Error when appending sample from OTLP: out of order sample')).toBeInTheDocument();
    expect(screen.getByText('error · 00:03:04 – 00:03:25 UTC · combined_appender.go:181')).toBeInTheDocument();
    expect(screen.queryByTestId('raw-logs')).not.toBeInTheDocument();
  });

  it('folds routine lines seen once behind a toggle', () => {
    render(<LogGroupsView logs='raw text' additionalInfo={groupedInfo} />);

    expect(screen.queryByText('WAL checkpoint complete')).not.toBeInTheDocument();
    fireEvent.click(screen.getByText('1 other line, each seen once'));
    expect(screen.getAllByTestId('log-groups-row')).toHaveLength(3);
    expect(screen.getByText('info · 01:00:30 UTC')).toBeInTheDocument();
    expect(screen.getByText('Hide lines seen once')).toBeInTheDocument();
  });

  it('omits an unknown level and a missing time from the meta line', () => {
    render(<LogGroupsView logs='raw text' additionalInfo={groupedInfo} />);

    const readyRow = screen.getByText('ready').parentElement;
    expect(readyRow.textContent).toBe('ready');
  });

  it('lists routine lines when nothing is notable', () => {
    const routineOnly = {
      log_total_lines: 30,
      log_groups: [
        { text: 'started', count: 1, level: 'info' },
        { text: 'listening', count: 1, level: 'info' },
      ],
    };
    render(<LogGroupsView logs='raw text' additionalInfo={routineOnly} />);

    expect(screen.getAllByTestId('log-groups-row')).toHaveLength(2);
    expect(screen.queryByTestId('log-groups-toggle-routine')).not.toBeInTheDocument();
  });

  it('reads epoch timestamps and shows unparsed ones as written', () => {
    const info = {
      log_total_lines: 30,
      log_groups: [
        { text: 'epoch seconds', count: 2, level: 'error', first_seen: '1789606984', last_seen: '1789606990.5' },
        { text: 'odd format', count: 2, level: 'error', first_seen: 'Sep17 x', last_seen: 'Sep17 x' },
      ],
    };
    render(<LogGroupsView logs='raw text' additionalInfo={info} />);

    expect(screen.getByText('error · 01:03:04 – 01:03:10 UTC')).toBeInTheDocument();
    expect(screen.getByText('error · Sep17 x')).toBeInTheDocument();
  });

  it('reveals the raw lines on request', () => {
    render(<LogGroupsView logs='raw text' additionalInfo={groupedInfo} />);

    fireEvent.click(screen.getByTestId('log-groups-toggle-raw'));
    expect(screen.getByTestId('raw-logs')).toHaveTextContent('raw text');
    expect(screen.getByText('Hide raw lines')).toBeInTheDocument();
  });

  it('shows the file itself when it is short', () => {
    render(<LogGroupsView logs='two lines' additionalInfo={{ ...groupedInfo, log_total_lines: RAW_LOG_LINE_LIMIT }} />);

    expect(screen.getByTestId('raw-logs')).toHaveTextContent('two lines');
    expect(screen.queryByTestId('log-groups')).not.toBeInTheDocument();
  });

  it('shows the file itself for evidence stored before grouping existed', () => {
    render(<LogGroupsView logs='old evidence' additionalInfo={{ pod_name: 'p' }} />);

    expect(screen.getByTestId('raw-logs')).toHaveTextContent('old evidence');
  });
});
