import { splitSavings } from '@components/optimise-new/rightSizingData';

// Per-entry figures the producers stamp on add_info; the row net is stored separately.
const entry = (resource: string, dollars?: number) => ({
  resource,
  allocated: { request: 1, limit: null },
  recommended: { request: 2, limit: null },
  ...(dollars === undefined ? {} : { add_info: { estimated_savings: dollars } }),
});

describe('splitSavings', () => {
  it('separates what the workload saves from what it pays, per container', () => {
    const split = splitSavings({
      app: [entry('cpu', 28.2), entry('memory', 12.1)],
      sidecar: [entry('cpu', -10), entry('memory', -29.93)],
    });
    expect(split?.gross).toBeCloseTo(40.3);
    expect(split?.cost).toBeCloseTo(39.93);
    expect(split?.net).toBeCloseTo(0.37);
    expect(split?.containers.map((c) => [c.name, Math.round(c.total)])).toEqual([
      ['app', 40],
      ['sidecar', -40],
    ]);
  });

  it('splits inside one container when cpu goes down and memory goes up', () => {
    const split = splitSavings({ app: [entry('cpu', 40), entry('memory', -40)] });
    expect(split?.gross).toBe(40);
    expect(split?.cost).toBe(40);
    expect(split?.containers[0]).toEqual({ name: 'app', cpu: 40, memory: -40, total: 0 });
  });

  it('is null for rows written before the producers stamped the figures', () => {
    expect(splitSavings({ app: [entry('cpu'), entry('memory')] })).toBeNull();
    expect(splitSavings(null)).toBeNull();
    expect(splitSavings({ notifications: [] })).toBeNull();
  });

  it('skips the entries that carry no figure but keeps the container', () => {
    const split = splitSavings({ app: [entry('cpu', 15), { resource: 'memory', info: 'No data' }] });
    expect(split?.containers[0]).toEqual({ name: 'app', cpu: 15, memory: null, total: 15 });
  });
});
