{{/*
Expand the chart name.
*/}}
{{- define "omsa.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "omsa.fullname" -}}
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

{{- define "omsa.namespace" -}}
{{- default .Release.Namespace .Values.namespaceOverride -}}
{{- end -}}

{{- define "omsa.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "omsa.labels" -}}
helm.sh/chart: {{ include "omsa.chart" . }}
{{ include "omsa.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "omsa.selectorLabels" -}}
app.kubernetes.io/name: {{ include "omsa.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "omsa.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "omsa.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "omsa.managerImage" -}}
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

{{- define "omsa.imagePullSecrets" -}}
{{- $pullSecrets := concat (.Values.global.imagePullSecrets | default list) (.Values.image.pullSecrets | default list) -}}
{{- if $pullSecrets }}
imagePullSecrets:
{{- range $pullSecrets }}
  - name: {{ .name | default . | quote }}
{{- end }}
{{- end }}
{{- end -}}

