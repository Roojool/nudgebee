import { ITEMS, MODULES, REGISTRY_PANELS, REGISTRY_VERSION } from '@lib/onboarding/setupRegistry';
import { ITEM_EVALUATORS } from '@lib/onboarding/itemEvaluators';
import { PANELS } from '@lib/onboardingReadiness';

// Guard tests for the registry. Every one of these turns a class of registry
// edit that would silently break the card into a failing build instead.

describe('registry integrity', () => {
  it('resolves every item id a panel names', () => {
    const missing = REGISTRY_PANELS.flatMap((p) => p.items.filter((id) => !ITEMS[id]).map((id) => `${p.id} → ${id}`));
    expect(missing).toEqual([]);
  });

  it('places every defined item in exactly one panel', () => {
    const placements = new Map<string, string[]>();
    REGISTRY_PANELS.forEach((p) => p.items.forEach((id) => placements.set(id, [...(placements.get(id) || []), p.id])));

    // An item in two panels would be evaluated twice and counted twice.
    expect([...placements].filter(([, panels]) => panels.length > 1)).toEqual([]);
    // An item defined but placed nowhere is dead weight that never renders.
    expect(Object.keys(ITEMS).filter((id) => !placements.has(id))).toEqual([]);
  });

  it('keys every item by its own id', () => {
    expect(Object.entries(ITEMS).filter(([key, item]) => item.id !== key)).toEqual([]);
  });

  // The invariant that actually matters: an item that has neither an evaluator
  // nor providers to match would render permanently blank, with nothing a user
  // could do to satisfy it.
  it('leaves no live item unevaluatable', () => {
    const stranded = Object.values(ITEMS)
      .filter((item) => !item.planned)
      .filter((item) => !ITEM_EVALUATORS[item.id] && item.providers.length === 0)
      .map((item) => item.id);
    expect(stranded).toEqual([]);
  });

  it('points every panel prerequisite at a panel that exists', () => {
    const ids = new Set(REGISTRY_PANELS.map((p) => p.id));
    expect(REGISTRY_PANELS.filter((p) => p.needs && !ids.has(p.needs)).map((p) => p.id)).toEqual([]);
  });

  it('carries a version to stamp on saved progress', () => {
    expect(REGISTRY_VERSION).toBeGreaterThan(0);
  });
});

describe('panel order', () => {
  // Settled in review, and a product decision rather than an implementation
  // detail — the reorder that moved triggers third passed every existing test,
  // which is exactly why it needs one of its own.
  it('is accounts → telemetry → triggers → ai → smarter → delivery', () => {
    expect(REGISTRY_PANELS.map((p) => p.id)).toEqual(['accounts', 'telemetry', 'triggers', 'ai', 'smarter', 'delivery']);
  });

  it('is the order the resolved PANELS render in', () => {
    expect(PANELS.map((p) => p.id)).toEqual(REGISTRY_PANELS.map((p) => p.id));
  });

  it('labels exactly one panel essential on screen', () => {
    expect(REGISTRY_PANELS.filter((p) => p.tagTier).map((p) => p.id)).toEqual(['accounts']);
  });
});

describe('the need matrix', () => {
  const moduleIds = MODULES.map((m) => m.id);

  it('gives every item a level for every module', () => {
    const gaps = Object.values(ITEMS).flatMap((item) => moduleIds.filter((m) => !item.need?.[m]).map((m) => `${item.id}.${m}`));
    expect(gaps).toEqual([]);
  });

  it('uses only req, opt and na', () => {
    const bad = Object.values(ITEMS).flatMap((item) =>
      Object.entries(item.need || {})
        .filter(([, level]) => !['req', 'opt', 'na'].includes(level))
        .map(([m, level]) => `${item.id}.${m} = ${level}`)
    );
    expect(bad).toEqual([]);
  });

  // Two properties the spec calls out as easy to break in a refactor. If either
  // fails it should be because someone decided to change it, not by accident.
  it('keeps Automation identical to Troubleshoot on every row', () => {
    const diverged = Object.values(ITEMS)
      .filter((item) => item.need?.ts !== item.need?.auto)
      .map((item) => `${item.id}: ts=${item.need?.ts} auto=${item.need?.auto}`);
    expect(diverged).toEqual([]);
  });

  it('keeps the gateway optional everywhere, while still standing in for the LLM', () => {
    expect(ITEMS.gateway.need).toEqual({ ts: 'opt', opt: 'opt', k8s: 'opt', auto: 'opt' });
    // Need level and substitutability are separate ideas: "optional" describes
    // how much a module wants it, not whether it can satisfy the llm step.
    expect(ITEMS.gateway.alt).toBe(true);
    expect(ITEMS.gateway.required).toBe(true);
  });

  it('requires an account for every module — the one thing setup insists on', () => {
    expect(ITEMS.account.need).toEqual({ ts: 'req', opt: 'req', k8s: 'req', auto: 'req' });
  });
});

describe('registry copy', () => {
  // app/CLAUDE.md: user-visible copy never spells the product or assistant name.
  // This table evaluates at import, before /api/public/app_config resolves, so a
  // literal here would latch the house brand on every white-label tenant.
  it('carries brand placeholders instead of product names', () => {
    const copy = [
      ...REGISTRY_PANELS.flatMap((p) => [p.title, p.sub]),
      ...Object.values(ITEMS).flatMap((i) => [i.name, i.purpose, i.why, ...(i.unlocks || []), ...i.meta.flat(), ...i.impact.flat()]),
    ].join(' | ');
    expect(copy).not.toMatch(/Nudgebee|Nubi/);
  });

  it('gives every item the copy the card renders', () => {
    Object.values(ITEMS).forEach((item) => {
      expect(item.purpose.length).toBeGreaterThan(0);
      expect(item.why.length).toBeGreaterThan(0);
      expect(item.meta.length).toBeGreaterThan(0);
      expect(item.impact.length).toBeGreaterThan(0);
    });
  });
});
