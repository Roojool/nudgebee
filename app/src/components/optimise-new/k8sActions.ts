import { safeParseJSON } from './utils';

// The three Kubernetes fixes the in-cluster agent can carry out besides pod
// right-sizing. Each has an adapter case on the backend already; what Optimize
// lacked was a confirm surface, so these rows sent the reader to the cluster
// page. The resolve modal is built around container CPU and memory, so each
// rule gets a small body of its own here instead.
export type K8sActionRule = 'unused_pvc' | 'pv_rightsize' | 'abandoned_resource';

// A typed-name action asks the reader to type the resource's name before it
// runs, as the cluster page does; that typed name is the acknowledgement for a
// removal. A size action takes a number and defers the acknowledgement to the
// shared safety step when the verdict asks for one.
export type K8sActionInput = 'typed-name' | 'size';

export interface K8sActionFact {
  label: string;
  value: string;
  copyable?: boolean;
}

export interface K8sAction {
  rule: K8sActionRule;
  /** Button label on the action bar and the modal title. */
  label: string;
  /** The confirm button's verb. */
  confirmText: string;
  input: K8sActionInput;
  /** What the reader must type (typed-name), or what is resized (size). */
  subject: string;
  /** One line above the facts saying what will happen. */
  summary: string;
  facts: K8sActionFact[];
  /** Starting value for a size input, in Gi. */
  defaultSize?: number;
  /** Rule-specific safeguard for the readiness banner; removals use the default. */
  safeguard?: string;
  /** Toast once the agent task is queued. */
  successMessage: string;
}

const str = (value: unknown): string => (value == null ? '' : String(value));

const workloadOf = (rec: any) => {
  const meta = rec?.cloud_resourse?.meta || {};
  return {
    name: str(rec?.resource_name || meta.controller || rec?.cloud_resourse?.name),
    namespace: str(rec?.resource_k8s_namespace || meta.namespace),
    kind: str(meta.controllerKind || rec?.resource_type),
  };
};

export const k8sActionFor = (rec: any): K8sAction | null => {
  if (!rec || rec.category !== 'RightSizing') return null;
  const payload = safeParseJSON(rec.recommendation) || {};

  switch (rec.rule_name as K8sActionRule) {
    case 'unused_pvc': {
      const name = str(payload?.metadata?.name);
      if (!name) return null;
      const claimNs = str(payload?.spec?.claimRef?.namespace);
      const claim = str(payload?.spec?.claimRef?.name);
      return {
        rule: 'unused_pvc',
        label: 'Reclaim Volume',
        confirmText: 'Delete Volume',
        input: 'typed-name',
        subject: name,
        summary: 'Deletes the volume and releases its storage. Data on it cannot be recovered afterwards.',
        facts: [
          { label: 'Volume', value: name, copyable: true },
          ...(payload?.spec?.capacity?.storage ? [{ label: 'Size', value: str(payload.spec.capacity.storage) }] : []),
          ...(claim ? [{ label: 'Last claim', value: claimNs ? `${claimNs}/${claim}` : claim }] : []),
        ],
        successMessage: `Reclaim of ${name} queued — follow it under Resolutions.`,
      };
    }
    case 'abandoned_resource': {
      const { name, namespace, kind } = workloadOf(rec);
      if (!name) return null;
      return {
        rule: 'abandoned_resource',
        label: 'Scale Down Workload',
        confirmText: 'Scale Down',
        input: 'typed-name',
        subject: name,
        summary: 'Scales the workload to zero replicas. Its pods stop; the manifest stays, so it can be scaled back up.',
        facts: [
          { label: 'Workload', value: name, copyable: true },
          ...(kind ? [{ label: 'Kind', value: kind }] : []),
          ...(namespace ? [{ label: 'Namespace', value: namespace }] : []),
        ],
        successMessage: `Scale-down of ${name} queued — follow it under Resolutions.`,
      };
    }
    case 'pv_rightsize': {
      const claim = str(payload?.spec?.claimRef?.name);
      const namespace = str(payload?.spec?.claimRef?.namespace || rec?.resource_k8s_namespace);
      if (!claim) return null;
      // Over half the findings name no size; the cluster page still offers
      // the resize with the field empty, and so does this.
      const recommended = Number(payload?.recommendation?.recommend_size);
      const hasRecommended = Number.isFinite(recommended) && recommended > 0;
      const current = str(payload?.spec?.capacity?.storage);
      return {
        rule: 'pv_rightsize',
        label: 'Resize Volume',
        confirmText: 'Resize Volume',
        input: 'size',
        subject: claim,
        summary: hasRecommended ? 'Resizes the claim to the size below.' : 'Resizes the claim to a size you choose; this finding names none.',
        facts: [
          { label: 'Claim', value: namespace ? `${namespace}/${claim}` : claim, copyable: true },
          ...(current ? [{ label: 'Current size', value: current }] : []),
          ...(hasRecommended ? [{ label: 'Recommended', value: `${recommended}Gi` }] : []),
        ],
        defaultSize: hasRecommended ? recommended : undefined,
        safeguard: 'Apply in a maintenance window — the workload mounting the claim may restart while it is resized.',
        successMessage: `Resize of ${claim} queued — follow it under Resolutions.`,
      };
    }
    default:
      return null;
  }
};

// The size a reader typed, as a positive number of Gi, or null when it is not
// one. The backend refuses a zero size and formats the number itself.
export const parseSizeGi = (value: string): number | null => {
  const n = Number(String(value).trim());
  return Number.isFinite(n) && n > 0 ? n : null;
};

export interface K8sApplyInput {
  typedName?: string;
  sizeGi?: number | null;
}

// The apply payload for a rule, or null while the reader's input does not yet
// permit the action. The backend reads only recommendedSize from it (for the
// resize); the rest is what the resolution row records as done.
export const buildK8sApplyData = (action: K8sAction, rec: any, input: K8sApplyInput): Record<string, unknown> | null => {
  if (action.input === 'typed-name') {
    if ((input.typedName ?? '') !== action.subject) return null;
    if (action.rule === 'abandoned_resource') {
      const { name, namespace, kind } = workloadOf(rec);
      return { name, namespace, kind, replica_count: 0 };
    }
    return { name: action.subject };
  }
  if (input.sizeGi == null) return null;
  const payload = safeParseJSON(rec?.recommendation) || {};
  return {
    name: action.subject,
    namespace: str(payload?.spec?.claimRef?.namespace || rec?.resource_k8s_namespace),
    recommendedSize: input.sizeGi,
  };
};
