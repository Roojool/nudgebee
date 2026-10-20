{{/*
Expand the name of the chart.
*/}}
{{- define "services-server.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "services-server.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "services-server.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "services-server.labels" -}}
helm.sh/chart: {{ include "services-server.chart" . }}
{{ include "services-server.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "services-server.selectorLabels" -}}
app.kubernetes.io/name: {{ include "services-server.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "services-server.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "services-server.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "imageName" -}}
{{- $registry := (default .Values.global.image.registry .Values.image.registry) -}}
{{- if $registry -}}
{{- if contains $registry .Values.image.repository -}}
{{- printf "%s:%s" .Values.image.repository .Values.image.tag -}}
{{- else -}}
{{- printf "%s/%s:%s" $registry .Values.image.repository .Values.image.tag -}}
{{- end -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository .Values.image.tag -}}
{{- end -}}
{{- end }}

{{- define "imageSidecarName" -}}
{{- $registry := (default .Values.global.image.registry .Values.image.registry) -}}
{{- if $registry -}}
{{- if contains $registry .Values.image.repository -}}
{{- printf "%s:%s" .Values.image.repository .Values.image.sidecarTag -}}
{{- else -}}
{{- printf "%s/%s:%s" $registry .Values.image.repository .Values.image.sidecarTag -}}
{{- end -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository .Values.image.sidecarTag -}}
{{- end -}}
{{- end }}

{{- define "nudgebee.secretName" -}}
  {{- $name := .Values.global.existingNudgebeeSecretName | default .Values.secretName -}}
  {{- default $name "nudgebee" -}}
{{- end -}}
{{/*
Worker selector labels.

The worker pods deliberately carry a DIFFERENT app.kubernetes.io/name
("<name>-worker") from the API pods. That is what keeps them out of the
services-server Service, whose selector is equality-only and so cannot exclude
them any other way. Renaming here rather than adding a component label to the
existing Deployment also avoids touching that Deployment's spec.selector, which
Kubernetes treats as immutable — an in-place `helm upgrade` would be rejected.
*/}}
{{- define "services-server.workerSelectorLabels" -}}
app.kubernetes.io/name: {{ include "services-server.name" . }}-worker
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Worker labels.
*/}}
{{- define "services-server.workerLabels" -}}
helm.sh/chart: {{ include "services-server.chart" . }}
{{ include "services-server.workerSelectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
SERVER_ROLE for a given deployment, or "" when the split is off.

Empty means the env var is not rendered at all and the binary falls back to its
own default ("all" — HTTP plus background jobs), which is exactly the historical
single-Deployment behaviour every OSS and on-prem install runs. So the split is
driven by one value, worker.enabled: either there are two deployments with
complementary roles, or there is one deployment doing everything. There is no
combination of values that leaves the queues with no consumer.

Call with a dict: (dict "ctx" $ "role" "api"|"worker").
*/}}
{{- define "services-server.serverRole" -}}
{{- if (.ctx.Values.worker | default dict).enabled -}}
{{- .role -}}
{{- end -}}
{{- end }}

{{/*
GOMEMLIMIT env entry computed from a resources block.

Soft-caps the Go heap at ~90% of the container memory limit so the runtime GCs
(and returns freed pages) aggressively as it approaches the cgroup cap, instead
of being OOM-killed. GOMEMLIMIT bounds only the Go heap, so the ~10% headroom
covers non-heap memory (goroutine stacks, runtime/GC metadata, freed-but-
unreturned pages) that would otherwise push total RSS over the limit and OOM
before a GC fires. Go does NOT read the cgroup memory limit itself (container
awareness is GOMAXPROCS/CPU only), so without this the runtime happily lets the
heap grow past the cap before a collection ever fires. Computed from
limits.memory because the downward API can't scale below 100% for the memory
resource — only integer/unit divisors are allowed.

Lives in a helper rather than inline so the API and worker deployments cannot
drift apart on it.

Call with a dict: (dict "ctx" $ "resources" <resources block>).
*/}}
{{- define "services-server.goMemLimitEnv" -}}
{{- /* Navigate limits.memory nil-safely: a bare .limits.memory panics if an
       operator overrides resources to {} or null, and Sprig's dig rejects
       .Values (chartutil.Values, not a plain map). Chaining `| default dict`
       yields "" when any level is missing. */}}
{{- $limits := (.resources | default dict).limits | default dict }}
{{- $limMem := $limits.memory | default "" | toString }}
{{- if and $limMem (not (hasKey (default dict .ctx.Values.env) "GOMEMLIMIT")) }}
{{- /* Match the Kubernetes quantity suffixes case-sensitively: binary Gi/Mi and
       decimal G/M (1G=1e9 B ≈ 953.674 MiB, 1M=1e6 B ≈ 0.953674 MiB). NOT
       lowercased — k8s quantities are case-sensitive, and lowercase "m" is
       milli (e.g. 3000m = 3 bytes), not mega. */}}
{{- $mib := 0 }}
{{- if hasSuffix "Gi" $limMem }}
{{-   $mib = mulf (trimSuffix "Gi" $limMem) 1024 0.9 | floor | int }}
{{- else if hasSuffix "Mi" $limMem }}
{{-   $mib = mulf (trimSuffix "Mi" $limMem) 0.9 | floor | int }}
{{- else if hasSuffix "G" $limMem }}
{{-   $mib = mulf (trimSuffix "G" $limMem) 953.674 0.9 | floor | int }}
{{- else if hasSuffix "M" $limMem }}
{{-   $mib = mulf (trimSuffix "M" $limMem) 0.953674 0.9 | floor | int }}
{{- end }}
{{- if gt $mib 0 }}
- name: GOMEMLIMIT
  value: "{{ $mib }}MiB"
{{- end }}
{{- end }}
{{- end }}
