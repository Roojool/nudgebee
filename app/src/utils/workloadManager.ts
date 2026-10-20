import { ANNOTATIONS } from '@lib/annotationKeys';

export interface OwnerReference {
  kind?: string;
  name?: string;
  controller?: boolean;
}

export interface WorkloadManager {
  /** Display name of whatever reconciles this workload back to its own source, or null. */
  manager: string | null;
  /** Git repo to raise a PR against, when an annotation names one. */
  gitRepo: string | null;
  /** Helm release name, when the workload carries Helm's ownership annotation. */
  helmRelease: string | null;
  /** The custom resource whose controller owns this workload, e.g. a VMAgent. */
  operator: { kind: string; name: string } | null;
}

// A controller owner of one of these kinds is Kubernetes' own hierarchy (a ReplicaSet owned by its
// Deployment). Any other controller owner is an operator that renders this object from its own
// resource and rewrites a manual edit on its next reconcile — usually within seconds.
const BUILTIN_OWNER_KINDS = new Set(['Deployment', 'ReplicaSet', 'StatefulSet', 'DaemonSet', 'Job', 'CronJob']);

// Detects whether a workload is declaratively managed (operator / Argo CD / Flux / Helm / GitOps)
// so an action that patches the live object can warn that the change will not stick. Returns the
// manager's display name and the git repo to raise a PR against, when either is known.
export const detectGitOpsManager = (
  annotations: Record<string, string> = {},
  labels: Record<string, string> = {},
  ownerReferences: OwnerReference[] = []
): WorkloadManager => {
  const annKeys = Object.keys(annotations || {});
  const labKeys = Object.keys(labels || {});
  const gitRepo = annotations?.[ANNOTATIONS.CI_GIT_REPO] || annotations?.[ANNOTATIONS.WORKLOAD_GIT_REPO] || null;
  const helmRelease = annotations?.['meta.helm.sh/release-name'] || null;
  const owner = (Array.isArray(ownerReferences) ? ownerReferences : []).find(
    (o) => o?.controller === true && typeof o?.kind === 'string' && !BUILTIN_OWNER_KINDS.has(o.kind)
  );
  const operator = owner ? { kind: owner.kind as string, name: owner.name || '' } : null;
  let manager: string | null = null;
  if (operator) {
    manager = operator.kind;
  } else if (annKeys.some((k) => k.startsWith('argocd.argoproj.io/')) || labels?.['argocd.argoproj.io/instance']) {
    manager = 'Argo CD';
  } else if ([...annKeys, ...labKeys].some((k) => k.includes('fluxcd.io'))) {
    manager = 'Flux';
  } else if (labels?.['app.kubernetes.io/managed-by'] === 'Helm' || helmRelease) {
    manager = 'Helm';
  } else if (gitRepo) {
    manager = 'GitOps';
  }
  return { manager, gitRepo, helmRelease, operator };
};
