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

{{/*
Expand the name of the chart.
*/}}
{{- define "k8s_collector.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "k8s_collector.fullname" -}}
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
{{- define "k8s_collector.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "k8s_collector.labels" -}}
helm.sh/chart: {{ include "k8s_collector.chart" . }}
{{ include "k8s_collector.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "k8s_collector.selectorLabels" -}}
app.kubernetes.io/name: {{ include "k8s_collector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "k8s_collector.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "k8s_collector.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "nudgebee.secretName" -}}
  {{- $name := .Values.global.existingNudgebeeSecretName | default .Values.secretName -}}
  {{- default $name "nudgebee" -}}
{{- end -}}
{{/*
OpenTelemetry environment.

Metrics are pushed over OTLP rather than scraped: the collector runs inside a
customer cluster, where a ServiceMonitor required the Prometheus Operator to be
installed and still left the samples in the customer's Prometheus with nothing
forwarding them to Nudgebee. Leaving otel.exporter empty disables metric export
entirely, so an install with no collector endpoint keeps working unchanged.
*/}}
{{- define "k8s_collector.otelEnv" -}}
- name: OTEL_SERVICE_NAME
  value: {{ .Values.otel.serviceName | default "k8s-collector" | quote }}
{{- if .Values.otel.exporter }}
- name: OTEL_EXPORTER
  value: {{ .Values.otel.exporter | quote }}
- name: OTEL_METRIC_EXPORT_INTERVAL
  value: {{ .Values.otel.exportIntervalMillis | default 60000 | quote }}
{{- end }}
{{- if eq .Values.otel.exporter "otlp" }}
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: {{ required "A valid otel.endpoint is required when otel.exporter is 'otlp'" .Values.otel.endpoint | quote }}
- name: OTEL_EXPORTER_OTLP_PROTOCOL
  value: {{ .Values.otel.protocol | default "grpc" | quote }}
{{- end }}
{{- end }}
