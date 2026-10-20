import { reportContainsInvestigation } from '../investigationReport';

// #38925: the Root Cause report is assembled from the investigation and opens with it verbatim, so
// the Investigation section would otherwise render the same text a second time.
describe('reportContainsInvestigation', () => {
  const investigation = '### Causality Chain\n- Root Cause: OOMKilled.';

  it('recognises a report that opens with the investigation and adds sections after it', () => {
    const report = `${investigation}\n\n### Affected Components\n- **Namespace:** \`nudgebee\``;

    expect(reportContainsInvestigation(report, investigation)).toBe(true);
  });

  it('ignores surrounding whitespace on either side', () => {
    expect(reportContainsInvestigation(`\n  ${investigation}\n\n### More\n- x`, `${investigation}\n `)).toBe(true);
  });

  it('keeps both sections when the report is a separate rewrite', () => {
    expect(reportContainsInvestigation('Full write-up: OOMKilled after a limit regression.', investigation)).toBe(false);
  });

  it('keeps both sections while the report is still pending', () => {
    expect(reportContainsInvestigation('', investigation)).toBe(false);
    expect(reportContainsInvestigation(null, investigation)).toBe(false);
  });

  it('is false when there is no investigation to fold away', () => {
    expect(reportContainsInvestigation('Some report.', '')).toBe(false);
    expect(reportContainsInvestigation('Some report.', null)).toBe(false);
    expect(reportContainsInvestigation('Some report.', '   ')).toBe(false);
  });
});
