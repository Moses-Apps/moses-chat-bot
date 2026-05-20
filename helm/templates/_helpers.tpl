{{/*
Expand the name of the chart.
*/}}
{{- define "moses-chat-bot.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name. We truncate at 63 chars because
some Kubernetes name fields are limited to that.
*/}}
{{- define "moses-chat-bot.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- printf "%s" $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Chart label.
*/}}
{{- define "moses-chat-bot.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "moses-chat-bot.labels" -}}
helm.sh/chart: {{ include "moses-chat-bot.chart" . }}
{{ include "moses-chat-bot.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
moses.ai/tenant-id: {{ .Values.moses.tenantId | quote }}
moses.ai/execution-id: {{ .Values.moses.executionId | quote }}
{{- if .Values.moses.chartId }}
moses.ai/chart-id: {{ .Values.moses.chartId | quote }}
{{- end }}
{{- if .Values.moses.appSlug }}
moses.ai/app-slug: {{ .Values.moses.appSlug | quote }}
{{- end }}
moses.ai/managed-by: moses
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "moses-chat-bot.selectorLabels" -}}
app.kubernetes.io/name: {{ include "moses-chat-bot.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Postgres service name (also used as host for DATABASE_URL).
*/}}
{{- define "moses-chat-bot.postgresFullname" -}}
{{- printf "%s-postgres" (include "moses-chat-bot.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Postgres secret name.
*/}}
{{- define "moses-chat-bot.postgresSecretName" -}}
{{- if .Values.postgres.auth.existingSecret }}
{{- .Values.postgres.auth.existingSecret }}
{{- else }}
{{- include "moses-chat-bot.postgresFullname" . }}
{{- end }}
{{- end }}

{{/*
Master encryption key Secret name.
*/}}
{{- define "moses-chat-bot.masterKeySecretName" -}}
{{- if .Values.masterKey.existingSecret }}
{{- .Values.masterKey.existingSecret }}
{{- else }}
{{- printf "%s-master-key" (include "moses-chat-bot.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
envFrom block — references every secret in .Values.secrets.secretNames.
Used by both deployments so the platform-issued multi-secret bundle
flows uniformly into frontend + backend.
*/}}
{{- define "moses-chat-bot.envFromSecrets" -}}
{{- range $name := .Values.secrets.secretNames }}
- secretRef:
    name: {{ $name | quote }}
{{- end }}
{{- end }}
