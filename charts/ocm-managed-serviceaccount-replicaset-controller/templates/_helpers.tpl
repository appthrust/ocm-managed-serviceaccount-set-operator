{{/*
Expand the chart name.
*/}}
{{- define "msars.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "msars.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "msars.namespace" -}}
{{- default .Release.Namespace .Values.namespaceOverride -}}
{{- end -}}

{{- define "msars.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "msars.metricsServiceName" -}}
{{- printf "%s-metrics" (include "msars.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "msars.labels" -}}
helm.sh/chart: {{ include "msars.chart" . }}
{{ include "msars.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "msars.selectorLabels" -}}
app.kubernetes.io/name: {{ include "msars.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "msars.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "msars.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "msars.managerImage" -}}
{{- $registry := .Values.image.registry -}}
{{- if .Values.global.imageRegistry -}}
{{- $registry = .Values.global.imageRegistry -}}
{{- end -}}
{{- $image := printf "%s/%s:%s" $registry .Values.image.repository .Values.image.tag -}}
{{- if .Values.image.digest -}}
{{- $image = printf "%s/%s@%s" $registry .Values.image.repository .Values.image.digest -}}
{{- end -}}
{{- $image -}}
{{- end -}}

{{- define "msars.imagePullSecrets" -}}
{{- $pullSecrets := concat (.Values.global.imagePullSecrets | default list) (.Values.image.pullSecrets | default list) -}}
{{- if $pullSecrets }}
imagePullSecrets:
{{- range $pullSecrets }}
  - name: {{ .name | default . | quote }}
{{- end }}
{{- end }}
{{- end -}}
