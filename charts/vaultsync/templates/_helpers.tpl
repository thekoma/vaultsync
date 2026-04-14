{{/*
Expand the name of the chart.
*/}}
{{- define "vaultsync.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "vaultsync.fullname" -}}
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
Common labels.
*/}}
{{- define "vaultsync.labels" -}}
helm.sh/chart: {{ include "vaultsync.chart" . }}
{{ include "vaultsync.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "vaultsync.selectorLabels" -}}
app.kubernetes.io/name: {{ include "vaultsync.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Chart label.
*/}}
{{- define "vaultsync.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "vaultsync.serviceAccountName" -}}
{{- if .Values.serviceAccount.name }}
{{- .Values.serviceAccount.name }}
{{- else }}
{{- include "vaultsync.fullname" . }}
{{- end }}
{{- end }}

{{/*
State namespace — defaults to release namespace.
*/}}
{{- define "vaultsync.stateNamespace" -}}
{{- default .Release.Namespace .Values.state.namespace }}
{{- end }}

{{/*
Image tag — defaults to appVersion.
*/}}
{{- define "vaultsync.imageTag" -}}
{{- default .Chart.AppVersion .Values.image.tag }}
{{- end }}
