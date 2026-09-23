# Workflow script pod isolation

## Overview

Every Kubernetes Job created by workflow-server's scripting executor is treated as
untrusted tenant-authored code. These Jobs receive no ambient Kubernetes or
cloud identity and cannot reach cloud instance metadata. Provider-specific
cloud access remains the responsibility of explicit, target-authorized task
executors; it is not added to arbitrary script Jobs.

The default policy blocks cloud metadata and cluster-internal destinations and
removes the Kubernetes token. User-defined Jobs have no reason to call
llm-server, workflow-server, or other internal services directly. Required
workflow integrations must use their authorized task paths instead.

## Threat model

The attacker can create or edit a workflow containing `scripting.run_script`,
`scm.github.cli`, or `scm.gitlab.cli`. They control the script and, for
`scripting.run_script`, may select a container image. Container hardening is
therefore not an authorization boundary.

On a shared Kubernetes node, untrusted code must not be able to obtain:

- the node's AWS instance-profile credentials;
- the GCE node service account or a GKE workload identity not explicitly
  assigned to the Job;
- the Azure VM or node-pool managed identity; or
- a Kubernetes API bearer token.

The common cloud metadata path is link-local `169.254.0.0/16`. Relevant known
endpoints include AWS, Azure, and GKE Dataplane V2 at `169.254.169.254`, plus
the GKE metadata-server path at `169.254.169.252`.

## Configuration

### Job identity

`runbook-server/internal/tasks/scripting/executors/k8s_executor.go` sets the
same immutable label on the Job and its pod template:

```yaml
nudgebee.io/workload: workflow-script
```

The label is set by workflow-server, not accepted from workflow input.
It covers all current callers of `KubernetesExecutor`, including script,
GitHub CLI, and GitLab CLI tasks.

### Ambient pod credentials

The generated PodSpec sets these fields:

```yaml
automountServiceAccountToken: false
hostNetwork: false
hostPID: false
hostIPC: false
enableServiceLinks: false
```

The container sets this fixed environment variable after user-provided `envFrom`:

```yaml
AWS_EC2_METADATA_DISABLED: "true"
```

The environment variable only prevents accidental AWS SDK fallback. The
NetworkPolicy is the security boundary because arbitrary code can unset the
variable and call metadata directly. Do not add a supposed equivalent for
`gcloud` or `az`: `gcloud`'s `auth/disable_credentials` disables all CLI
authentication, including intentionally supplied credentials, while Azure's
managed-identity exclusions are SDK-specific and do not constrain arbitrary
CLI commands or direct HTTP calls. The metadata-deny policy covers all three
providers.

Do not add a cloud-enabled service account to these Jobs. If a future workflow
task needs AWS, GCP, or Azure access, it must use a dedicated executor that
resolves and authorizes the requested target outside this untrusted pod.

### Restricted-egress NetworkPolicy

The workflow-server chart policy is enabled by default with generic link-local,
private, CGNAT, and loopback exclusions. Installations may add cluster-specific
internal CIDRs where necessary:

```yaml
scriptPodNetworkPolicy:
  enabled: true
  annotations: {}
  internalCIDRs: []
  dnsNamespaceSelector:
    kubernetes.io/metadata.name: kube-system
  dnsPodSelector:
    k8s-app: kube-dns
```

The policy allows DNS and public IPv4 egress except the configured internal and
metadata CIDRs:

```yaml
spec:
  podSelector:
    matchLabels:
      nudgebee.io/workload: workflow-script
  policyTypes: [Ingress, Egress]
  ingress: []
  egress:
    - ports:
        - {protocol: UDP, port: 53}
        - {protocol: TCP, port: 53}
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
            except:
              - 169.254.0.0/16
              - 10.0.0.0/8
              - 172.16.0.0/12
              - 192.168.0.0/16
              - 100.64.0.0/10
              - 127.0.0.0/8
```

The template allows DNS traffic on port 53 to pods selected as kube-dns in
`kube-system` by default. Operators can change these selectors to match their
cluster DNS deployment.

Ingress is denied because Jobs are not servers. Public egress remains allowed
for package downloads and SCM APIs. Service IP ranges can be outside RFC1918,
and CNIs differ in whether policy sees the Service VIP or the translated Pod
IP. Dev GKE's Service CIDR is `34.118.224.0/20`, but live testing showed the
generic exclusions block its Services after translation. Clusters where that
is not true must add their actual Service and Pod CIDRs to `internalCIDRs`.
Keep DNS restricted to the cluster DNS pods. Do not add an unrestricted egress
exception that reopens metadata or internal Services.

### Cluster enforcement

A rendered NetworkPolicy has no effect unless the cluster network provider
enforces it. Roll out enforcement separately in every environment:

- EKS: enable the VPC CNI network-policy agent on a supported add-on version.
- GKE: verify NetworkPolicy or Dataplane V2 enforcement is active.
- On-premises: document a supported enforcing CNI as a security prerequisite.

Policy enforcement should be enabled in a staging environment first. Enabling
it may activate previously installed policies that have never been exercised.

## Out of scope

- Moving workflow Jobs to dedicated nodes.
- Adding AWS, GCP, or Azure credentials to arbitrary script Jobs.
- Parsing scripts to decide whether cloud commands are safe.
- Relying on `AWS_EC2_METADATA_DISABLED` as the security boundary.
- Migrating customer AWS trust policies as part of this change.

## Rollout

1. Inventory existing NetworkPolicies. Enable enforcement in a non-production
   cluster and verify existing workspace and service traffic.
2. Deploy the executor label, PodSpec hardening, and default workflow-script
   policy. Probe metadata and internal Services from a test pod; add
   `internalCIDRs` if the generic exclusions do not block them.
3. Run the acceptance checks below for script, GitHub CLI, and GitLab CLI Jobs.
4. Enable enforcement in production clusters using a canary node pool or
   cluster-specific staged rollout where available.
5. Observe Job failures and denied-flow telemetry. Roll back enforcement, not
   the pod label or token hardening, if an unrelated dormant policy breaks
   traffic.
6. Investigate any workflows that depended on direct private or in-cluster
   access and move them to authorized task paths before enforcing in production.

## Acceptance criteria

### Render and unit checks

- A generated Job and pod template both carry
  `nudgebee.io/workload=workflow-script`.
- `automountServiceAccountToken` is explicitly false.
- Host namespace fields are explicitly false.
- `AWS_EC2_METADATA_DISABLED=true` is present and cannot be overridden by
  workflow-provided environment values.
- Helm rendering includes a NetworkPolicy whose selector exactly matches the
  generated pod label and whose IPv4 allow rule excludes link-local, RFC1918,
  CGNAT, loopback, and any configured `internalCIDRs`.
- Explicitly disabling `scriptPodNetworkPolicy.enabled` removes only the policy
  resource, not the PodSpec hardening.

### Runtime checks

Run these from an actual Kubernetes-executor workflow Job, not from the
workflow-server Deployment:

- AWS metadata token and credential requests fail.
- GCE/GKE metadata requests with `Metadata-Flavor: Google` fail.
- Azure identity-token metadata requests with `Metadata: true` fail.
- `llm-server`, `workflow-server`, and another unlisted internal Service fail
  by DNS name and direct ClusterIP; public HTTPS and DNS still work.
- `/var/run/secrets/kubernetes.io/serviceaccount/token` does not exist.
- `gh` and `glab` tasks can still reach their configured public endpoints.
- A normal public HTTPS script succeeds and DNS resolves.
- cloud-collector, llm-server, and other legitimate cloud-aware services on the
  same shared nodes retain their current access.

Record the cluster, CNI enforcement state, Job name, pod name, node name, and
command result for each runtime check. A rendered policy without these runtime
checks is not considered mitigation.

### Dev GKE network-policy probes (2026-09-23)

On dev GKE (`gke_nudgebee-dev_us-central1_nudgebee-dev`, namespace
`nudgebee`), the existing `llm-server-workspace` policy selected a running
workspace pod. From that pod, HTTP connections to `169.254.169.254:80`,
`169.254.169.252:988`, and the unlisted `workflow-server:8000` timed out;
`llm-server:8000/health` and `https://example.com/` returned HTTP 200.

A temporary pod and policy named `codex-workflow-meta-test` then exercised the
preliminary metadata-only IPv4 rule, selecting only that pod. From the temporary pod,
both metadata addresses timed out, while `workflow-server:8000/health`,
`llm-server:8000/health`, and `https://example.com/` all returned HTTP 200.
The Kubernetes service-account token path was absent. The temporary pod and
policy were deleted and their absence verified.

This records the preliminary rule's behavior on that GKE cluster at probe time.
It does not validate a workflow-created Job, EKS VPC CNI enforcement, other
GKE clusters, IPv6, or cloud credential selection.

To verify internal-service isolation, a second
temporary pod and policy (`codex-workflow-internal-test`) excluded link-local,
RFC1918, CGNAT, loopback, and the dev GKE Service CIDR
`34.118.224.0/20`. The cluster's Pod CIDR is `10.64.0.0/14` and its
NetworkPolicy provider is Calico. From that pod, both metadata endpoints,
`workflow-server:8000`, `llm-server:8000`, and the direct workflow-server
ClusterIP `34.118.235.143:8000` timed out; `https://example.com/` returned
HTTP 200. The temporary pod and policy were deleted. This is the behavior the
workflow Job policy must reproduce.

A third temporary pod and policy (`codex-workflow-dns-test`) used the chart's
default kube-system/kube-dns selectors. DNS resolved both targets, the
workflow-server connection timed out, and public HTTPS returned HTTP 200.
Both temporary resources were deleted.

A fourth temporary pod and policy (`codex-workflow-generic-test`) omitted the
dev GKE Service CIDR and used only the generic exclusions plus scoped DNS.
Connections to metadata, workflow-server and llm-server by Service name, the
direct workflow-server ClusterIP, and a direct workspace Pod IP timed out;
`https://example.com/` returned HTTP 200. This supports shipping the generic
rules as the dev GKE default. The temporary resources were deleted and their
absence verified. Other CNI implementations still need the same negative
test before the policy is considered effective there.

## Residual risk

- An explicitly supplied cloud credential in workflow environment input remains
  usable. Secret authorization and redaction are separate controls.
- IPv6 metadata/link-local access is not covered by the IPv4 policy. Dual-stack
  clusters need an IPv6 rule appropriate to the provider and CNI before this is
  considered complete there.
- NetworkPolicy behavior around node-local traffic is CNI-specific. Each
  supported deployment model requires a live negative metadata test.
- A privileged or host-network pod could bypass these controls. Admission policy
  should deny those fields for workflow Jobs; the current executor does not
  expose them, but admission provides defense against future regressions.
- Other tenant-controlled pod creators need their own selectors and policies;
  this policy only covers `KubernetesExecutor` Jobs.

## Follow-up identity work

This containment does not require provider-native authentication inside
arbitrary scripts. Longer-term work should instead give trusted platform
services purpose-specific workload identities and remove broad customer-role
assumption from node identities. AWS customer trust-policy migration remains a
separate project because it changes onboarding and existing customer stacks.
