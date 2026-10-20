/**
 * How each item answers "is this connected?".
 *
 * Almost every item answers it the same way: match the providers it declares in
 * the registry against the integrations the tenant actually has. That is
 * `providerMatchEvaluator`, and it is the default — so **adding a provider-backed
 * item needs no code at all**, just a registry entry.
 *
 * `ITEM_EVALUATORS` holds the exceptions: items whose connectedness cannot be
 * derived that way. Today that is `account` alone, which reads cloud accounts
 * and the K8s agent-health table rather than the integrations map.
 *
 * A guard test enforces the invariant that matters: every live item is
 * evaluatable — it either names an evaluator here, or declares providers the
 * default can match. An item that does neither would render permanently blank
 * with no way to satisfy it.
 */
import {
  accountRowState,
  type DetectedSignals,
  type OnboardingAccount,
  type OnboardingItemDef,
  type OnboardingState,
} from '@lib/onboardingReadiness';
import { ONBOARDING_CLOUD_PROVIDERS } from './setupRegistry';

/** Everything an evaluator is allowed to look at. */
export interface EvaluatorContext {
  /** Non-demo accounts on a provider we recognise, already filtered. */
  realAccounts: OnboardingAccount[];
  /** `cloud_provider` values of those accounts, e.g. ['AWS', 'K8s']. */
  connectedProviders: string[];
  /** Integration type → its state, unioned with the messaging-platform list. */
  stateByType: Map<string, OnboardingState>;
  /** Telemetry the agent resolved on its own, keyed by signal id. */
  detected: DetectedSignals;
  /** K8s agent health by account id; null until the lookup resolves. */
  k8sAgentConnected: Record<string, boolean> | null;
  /** Whether the tenant has at least one knowledge base, tenant-wide. `null`
   * until the lookup resolves — never inferred from AI/LLM being connected,
   * which is a prerequisite panel, not the same fact. */
  hasKnowledgeBase: boolean | null;
  /** Worst-wins comparison, so one failure is never hidden by a healthy sibling. */
  worst: (a: OnboardingState, b: OnboardingState) => OnboardingState;
  /** Maps a raw status string to a state; shared so the rules stay in one place. */
  stateOfStatus: (status: unknown) => OnboardingState;
  normalize: (value: unknown) => string;
}

/** What an evaluator returns — the state plus what it is connected by. */
export interface ItemVerdict {
  state: OnboardingState;
  connectedBy: string[];
  /** Each `connectedBy` label's own state — see `OnboardingItemStatus`'s
   *  own doc comment for why this exists (known issue #23). */
  connectedByState?: Record<string, OnboardingState>;
  /** True when the agent found it without the user configuring anything. */
  autoDetected: boolean;
  /** The real account rows behind `connectedBy` — populated only by
   *  `accountEvaluator`. A provider-label chip can't tell two AWS accounts
   *  apart, or say which one is still waiting on its agent; this can. */
  accounts?: ItemAccountRow[];
}

/** One connected account, named and stated for a per-account chip. */
export interface ItemAccountRow {
  id: string;
  label: string;
  cloudProvider: string;
  state: OnboardingState;
}

export type ItemEvaluator = (ctx: EvaluatorContext, item: OnboardingItemDef) => ItemVerdict;

/**
 * The default. An item is connected when any provider it declares appears in the
 * tenant's integrations; its state is the worst across everything that matched,
 * so a failing connection is never masked by a healthy one.
 */
export const providerMatchEvaluator: ItemEvaluator = (ctx, item) => {
  const matched = item.providers.filter((p) => (p.types || []).some((t) => ctx.stateByType.has(ctx.normalize(t))));
  const stateOfProvider = (p: (typeof matched)[number]): OnboardingState =>
    (p.types || []).reduce<OnboardingState>((s, t) => ctx.worst(s, ctx.stateByType.get(ctx.normalize(t)) ?? 'blank'), 'blank');
  const matchedState = matched.reduce<OnboardingState>((acc, p) => ctx.worst(acc, stateOfProvider(p)), 'blank');
  // Per-provider, not just the item's aggregate — a healthy Loki chip must not
  // render critical just because its sibling Elasticsearch integration is
  // broken (known issue #23). Keyed by label to match `connectedBy` itself.
  const connectedByState: Record<string, OnboardingState> = {};
  matched.forEach((p) => {
    connectedByState[p.label] = stateOfProvider(p);
  });

  // An agent-resolved signal is `done` carrying a chip — never a fifth state,
  // and never an error, because the capabilities call cannot report one. But
  // it still goes through `worst()` against a manually-added sibling on the
  // same item, same as any other pair of providers: otherwise a broken manual
  // integration goes invisible the moment auto-detection also fires, which is
  // exactly the masking this function's own doc comment above promises never
  // happens.
  const detectedProvider = ctx.detected[item.id];
  if (detectedProvider) {
    const state = ctx.worst('done', matchedState);
    if (state === 'done') return { state, connectedBy: [detectedProvider], connectedByState: { [detectedProvider]: 'done' }, autoDetected: true };
    // Something else on this item needs attention — show it instead of
    // claiming "nothing to configure", same as any other matched provider.
    return {
      state,
      connectedBy: [...matched.map((p) => p.label), detectedProvider],
      connectedByState: { ...connectedByState, [detectedProvider]: 'done' },
      autoDetected: false,
    };
  }

  if (matched.length === 0) return { state: 'blank', connectedBy: [], autoDetected: false };
  return { state: matchedState, connectedBy: matched.map((p) => p.label), connectedByState, autoDetected: false };
};

/**
 * Accounts are the one item the integrations map cannot answer for.
 *
 * The item's own `state` answers "has an account actually finished
 * connecting" — the setup milestone. A row existing is not enough on its
 * own: a K8s account whose in-cluster agent has never checked in is not a
 * milestone reached, so it must not check the whole item off (this was this
 * function's own earlier, wrong answer — see known-issues.md).
 *
 * `state` is `done` the moment *any one* account's own `rowState` is `done`
 * — not every account. A second account still pending or broken is real and
 * stays visible per-row (`accounts`, below), but it must not hold the whole
 * item at "not done" once one account genuinely works, the same way a
 * second broken integration on another item doesn't un-connect a healthy
 * one. Only when nothing has succeeded yet does worst-wins decide what to
 * show (a real error surfaces over a merely-pending row).
 */
export const accountEvaluator: ItemEvaluator = (ctx) => {
  if (ctx.realAccounts.length === 0) return { state: 'blank', connectedBy: [], autoDetected: false };

  // Per-row — feeds the per-account chip, the provider tile's "Connected"
  // badge, and now the aggregate `state` above too (all three must agree on
  // what "this one account" actually is). `accountRowState` is shared with
  // `SetupGateContext`'s own lock, so there is exactly one such definition.
  const rowState = (a: OnboardingAccount): OnboardingState => accountRowState(a, ctx.k8sAgentConnected);

  const state = ctx.realAccounts.some((a) => rowState(a) === 'done')
    ? 'done'
    : ctx.realAccounts.reduce<OnboardingState>((acc, a) => ctx.worst(acc, rowState(a)), 'blank');

  // `connectedProviders` holds `cloud_provider` codes ('K8s', 'SelfHosted', …) —
  // every other evaluator's `connectedBy` is display labels, which is what the
  // generic ProviderTile/ProviderPicker "Connected" badge compares against
  // (`connectedLabels.includes(p.label)`). AWS/GCP/Azure's code and label are
  // identical strings, which is what let this go unnoticed — K8s ('K8s' vs
  // 'Kubernetes') and Self-hosted ('SelfHosted' vs 'Self-hosted') never matched,
  // so those two tiles never showed as connected even once truly connected.
  const connectedBy = ONBOARDING_CLOUD_PROVIDERS.filter((p) => ctx.connectedProviders.includes(p.provider)).map((p) => p.label);

  // One row per real account, not per provider — lets the card name which
  // specific account (of several on the same provider) is still pending,
  // which a single "AWS" provider-label chip can't.
  const accounts: ItemAccountRow[] = ctx.realAccounts.map((a, i) => ({
    id: a.id || `${ctx.normalize(a.cloud_provider)}-${i}`,
    label: a.account_name || 'Untitled',
    cloudProvider: a.cloud_provider || '',
    state: rowState(a),
  }));

  return { state, connectedBy, autoDetected: false, accounts };
};

/**
 * Skills is Knowledge Base under different copy — the onboarding item names
 * the capability in the user's terms ("tell it once what 'prod' means here"),
 * the product feature underneath is the one already shipped in
 * `KnowledgeBaseTab.jsx` (`apiKnowledgeBase.getKnowledgeBases`, tenant-wide).
 * No provider row can match this the way telemetry/integrations do — a
 * knowledge base is not an `integrations_list` row — so it needs its own
 * evaluator rather than a registry entry.
 *
 * Strict on purpose: `done` only when a real knowledge base exists for a real
 * account, never inferred from the `ai` panel (its own prerequisite) being
 * connected. Having an LLM configured is not having written anything down for
 * it to use — those are two different facts, and treating the first as proof
 * of the second is exactly the false-green this item must not show.
 */
export const skillsEvaluator: ItemEvaluator = (ctx) => {
  if (ctx.hasKnowledgeBase === null) return { state: 'blank', connectedBy: [], autoDetected: false };
  return {
    state: ctx.hasKnowledgeBase ? 'done' : 'blank',
    connectedBy: ctx.hasKnowledgeBase ? ['Knowledge base'] : [],
    autoDetected: false,
  };
};

/** Exceptions only. Anything absent here uses `providerMatchEvaluator`. */
export const ITEM_EVALUATORS: Record<string, ItemEvaluator> = {
  account: accountEvaluator,
  skills: skillsEvaluator,
};

export const evaluatorFor = (itemId: string): ItemEvaluator => ITEM_EVALUATORS[itemId] ?? providerMatchEvaluator;
