// #38925: the Root Cause report (`detailed_response`) is assembled from the investigation and opens
// with it verbatim, then appends the affected components, code findings and firing history. Any
// surface that renders both the report and the investigation would therefore print the same text
// twice.
//
// This is a cross-service contract, not a component detail: the Go composer produces the shape, the
// Slack poster trims the same prefix before its closing message, and the investigate view and the
// remediation panel both ask this question. It lives here so those callers cannot drift apart.
//
// Returns false while the report is still being produced, and for older analyses where the report
// was a separate rewrite rather than an assembly — in both cases the two texts genuinely differ and
// both are worth showing.
export function reportContainsInvestigation(report?: string | null, investigation?: string | null): boolean {
  const text = (investigation || '').trim();
  return !!text && (report || '').trim().startsWith(text);
}
