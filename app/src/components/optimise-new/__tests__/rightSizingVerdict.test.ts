import { summarizeRightSizing } from '../rightSizingData';
import { buildInterpretation } from '../interpretation/buildInterpretation';

// The verdict names the action and which requests move. The current →
// recommended figures belong to the "Recommended Resource Changes" table
// directly below it, which also carries the percentages, so repeating them here
// made the reader parse the same four numbers twice.
const entry = (resource: string, allocated: number | null, recommended: number) => ({
  resource,
  allocated: allocated == null ? undefined : { request: allocated, limit: allocated },
  recommended: { request: recommended, limit: recommended },
});

const verdictFor = (recData: any, savings = 5) =>
  buildInterpretation({ category: 'RightSizing', ruleName: 'pod_right_sizing', savings, recData } as any).verdict;

describe('pod right-sizing verdict', () => {
  it('names the action and the resources, not the values', () => {
    const recData = { app: [entry('cpu', 1, 0.5), entry('memory', 4 * 1024 ** 3, 2 * 1024 ** 3)] };
    expect(verdictFor(recData)).toBe('Over-provisioned — reduce CPU and memory requests');
  });

  it('names only the resource that actually moves', () => {
    expect(verdictFor({ app: [entry('cpu', 1, 0.5)] })).toBe('Over-provisioned — reduce CPU requests');
    expect(verdictFor({ app: [entry('memory', 4 * 1024 ** 3, 8 * 1024 ** 3)] })).toBe('Under-provisioned — increase memory requests');
  });

  it('reads as setting requests when none were set', () => {
    expect(verdictFor({ app: [entry('cpu', null, 0.5)] })).toBe('No requests set — set CPU requests');
  });

  it('names the container only when there is more than one — a single row already names itself', () => {
    const multi = {
      app: [entry('cpu', 1, 0.5)],
      sidecar: [entry('cpu', 1, 2)],
    };
    expect(verdictFor(multi)).toContain(' on app (+1 more)');
    expect(verdictFor({ app: [entry('cpu', 1, 0.5)] })).not.toContain(' on ');
  });

  it('carries no current-or-recommended figure, whichever way the change goes', () => {
    for (const recData of [
      { app: [entry('cpu', 1, 0.5), entry('memory', 4 * 1024 ** 3, 2 * 1024 ** 3)] },
      { app: [entry('cpu', 0.5, 1)] },
      { app: [entry('cpu', 1, 0.5), entry('memory', 2 * 1024 ** 3, 4 * 1024 ** 3)] },
    ]) {
      expect(verdictFor(recData)).not.toMatch(/→|\d/);
    }
  });

  it('still exposes the figures to the table through changeText', () => {
    const summary = summarizeRightSizing({ app: [entry('cpu', 1, 0.5)] });
    expect(summary?.changeText).toContain('→');
    expect(summary?.changedResources).toEqual(['cpu']);
  });
});

describe('pod right-sizing impact line', () => {
  const impactFor = (recData: any, savings: number) =>
    buildInterpretation({ category: 'RightSizing', ruleName: 'pod_right_sizing', savings, recData } as any).impact;

  // Mixed row: CPU comes down, memory goes up, so splitSavings reports both
  // sides. The per-entry figure is what the producers stamp on add_info.
  const priced = (resource: string, allocated: number, recommended: number, saving: number) => ({
    resource,
    allocated: { request: allocated, limit: allocated },
    recommended: { request: recommended, limit: recommended },
    add_info: { estimated_savings: saving },
  });

  it('names which container saves and which costs more, without repeating the amounts', () => {
    const recData = {
      api: [priced('cpu', 1, 0.5, 20)],
      sidecar: [priced('memory', 1024 ** 3, 4 * 1024 ** 3, -30)],
    };
    const impact = impactFor(recData, -10);
    expect(impact).toBe('Saves on api · costs more on sidecar.');
    expect(impact).not.toMatch(/\$\d/);
  });

  it('drops the slot for a single container — the header states the split and the table shows it', () => {
    const recData = { api: [priced('cpu', 1, 0.5, 20), priced('memory', 1024 ** 3, 4 * 1024 ** 3, -30)] };
    expect(impactFor(recData, -10)).toBeUndefined();
  });

  it('always states a straight cost increase, which the panel must never hide', () => {
    const recData = { api: [priced('cpu', 0.5, 1, -6)] };
    expect(impactFor(recData, -6)).toContain('Costs ~$6/mo more');
  });
});
