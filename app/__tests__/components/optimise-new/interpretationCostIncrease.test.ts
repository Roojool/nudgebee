import { buildInterpretation } from '@components/optimise-new/interpretation/buildInterpretation';

// Payload shapes copied from live pod_right_sizing rows: the workload JSONB is
// { container: [entry, ...] } with allocated/recommended requests per resource.
const entry = (resource: string, allocated: number, recommended: number) => ({
  resource,
  allocated: { request: allocated, limit: null },
  recommended: { request: recommended, limit: null },
});

// nudgebee-agent-prod-opentelemetry-collector: CPU 250m -> 1.825, Mem 512Mi -> 1.25Gi.
const UNDER_PROVISIONED = {
  'opentelemetry-collector': [entry('cpu', 0.25, 1.825), entry('memory', 536870912, 1342177280)],
};

// services-server: CPU 300m -> 229m but Mem 1.98Gi -> 7.76Gi, so the net is a cost increase.
const MIXED_NET_INCREASE = {
  'services-server': [entry('cpu', 0.3, 0.229), entry('memory', 2122317824, 8327790592)],
};

const OVER_PROVISIONED = {
  app: [entry('cpu', 1.0, 0.25), entry('memory', 2147483648, 536870912)],
};

// The per-entry figures the producers stamp: the app saves, the sidecar pays,
// and the row net is a few cents.
const priced = (e: ReturnType<typeof entry>, dollars: number) => ({ ...e, add_info: { estimated_savings: dollars } });
const MIXED_NET_ZERO = {
  app: [priced(entry('cpu', 1.0, 0.4), 28.2), priced(entry('memory', 2147483648, 1073741824), 12.1)],
  sidecar: [priced(entry('cpu', 0.1, 0.25), -10), priced(entry('memory', 268435456, 805306368), -29.93)],
};

const build = (recData: unknown, savings: number) =>
  buildInterpretation({
    category: 'RightSizing',
    ruleName: 'pod_right_sizing',
    title: 'Pod Right Sizing',
    description: '',
    insight: '',
    savings,
    recData,
  });

describe('buildInterpretation cost impact', () => {
  it('names the monthly cost of a recommendation that raises requests', () => {
    const { impact } = build(UNDER_PROVISIONED, -38.13);
    expect(impact).toContain('Costs ~$38/mo more');
    expect(impact).toContain('prevents CPU throttling and OOM kills');
    expect(impact).not.toContain('No direct cost change');
  });

  it('reports the net cost when one resource drops but the other rises', () => {
    const { impact } = build(MIXED_NET_INCREASE, -16.01);
    expect(impact).toContain('Costs ~$16/mo more');
    expect(impact).not.toContain('No direct cost change');
  });

  it('does not style a cost increase as money saved', () => {
    // impactIsCost renders the line green and semibold — reserved for savings.
    expect(build(UNDER_PROVISIONED, -38.13).impactIsCost).toBe(false);
  });

  it('still reports real savings for a workload that is over-provisioned', () => {
    const { impact, impactIsCost } = build(OVER_PROVISIONED, 212.98);
    expect(impact).toContain('$213/mo savings');
    expect(impactIsCost).toBe(true);
  });

  it('leaves a sub-dollar cost change as no direct cost change', () => {
    expect(build(UNDER_PROVISIONED, -0.4).impact).toContain('No direct cost change');
  });

  it('names the cost increase on non-pod recommendations too', () => {
    const { impact, impactIsCost } = buildInterpretation({
      category: 'RightSizing',
      ruleName: 'aws_rds_overutilized',
      title: 'RDS Overutilized',
      description: '',
      insight: '',
      savings: -42,
      recData: {},
    });
    expect(impact).toContain('Costs ~$42/mo more to apply');
    expect(impactIsCost).toBe(false);
  });

  it('names both sides of a mixed workload instead of "no direct cost change"', () => {
    const { impact, impactIsCost } = build(MIXED_NET_ZERO, 0.37);
    expect(impact).toBe('Saves ~$40/mo on app · costs ~$40/mo more on sidecar · net ~$0/mo.');
    expect(impactIsCost).toBe(false);
  });

  it('states the net direction when a mixed workload still saves overall', () => {
    const payload = { ...MIXED_NET_ZERO, sidecar: [priced(entry('cpu', 0.1, 0.25), -10)] };
    const { impact, impactIsCost } = build(payload, 30.3);
    expect(impact).toBe('Saves ~$40/mo on app · costs ~$10/mo more on sidecar · net saves ~$30/mo.');
    expect(impactIsCost).toBe(true);
  });

  it('names the one container that both saves and pays when cpu goes down and memory goes up', () => {
    const payload = { app: [priced(entry('cpu', 1.0, 0.4), 40), priced(entry('memory', 1073741824, 2147483648), -40)] };
    const { impact } = build(payload, 0);
    expect(impact).toBe('Saves ~$40/mo and costs ~$40/mo more on app · net ~$0/mo.');
  });
});
