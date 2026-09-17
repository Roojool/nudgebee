import React from 'react';
import { render, screen } from '@testing-library/react';
import { KpiRow } from '@components/llm/cost-analyser/components/KpiRow';
import type { KpiTotals } from '@components/llm/cost-analyser/format';

const totals: KpiTotals = {
  totalCost: 10,
  runs: 5,
  avgCostPerRun: 2,
  inputTokens: 1000,
  outputTokens: 500,
  openAnomalies: 0,
  cacheHitRatePct: 40,
  cacheSavingsUsd: 3,
};

describe('KpiRow — Total AI cost vs Cache storage (#37832)', () => {
  it('folds cache storage into the hero total when no incompatible filter is active', () => {
    render(<KpiRow current={totals} previous={totals} storageCost={5} hasIncompatibleFilters={false} />);
    expect(screen.getByText('$15.00')).toBeInTheDocument(); // 10 + 5
  });

  it('excludes cache storage from the hero total under a Source/Agent/Status/User filter', () => {
    render(<KpiRow current={totals} previous={totals} storageCost={5} hasIncompatibleFilters={true} />);
    expect(screen.getByText('$10.00')).toBeInTheDocument(); // storage excluded
    expect(screen.queryByText('$15.00')).not.toBeInTheDocument();
  });

  it('always shows the real Cache storage figure, labeled Account-wide, regardless of filters', () => {
    render(<KpiRow current={totals} previous={totals} storageCost={5} hasIncompatibleFilters={true} />);
    expect(screen.getByText('$5.00')).toBeInTheDocument();
    expect(screen.getByText('Account-wide')).toBeInTheDocument();
  });
});
