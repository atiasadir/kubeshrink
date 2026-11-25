{{/* SPDX-License-Identifier: Apache-2.0 */}}

{{/* Chart name, overridable. */}}
{{- define "kubeshrink.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Release name, plus the chart name unless the release name contains it. */}}
{{- define "kubeshrink.fullname" -}}
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

{{- define "kubeshrink.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubeshrink.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kubeshrink.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "kubeshrink.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Image reference: repository:tag, or repository[:tag]@digest when image.digest is set. */}}
{{- define "kubeshrink.image" -}}
{{- $repo := .Values.image.repository }}
{{- with .Values.image.digest }}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" .) }}
{{- fail (printf "image.digest must be sha256: followed by 64 lowercase hex digits, not %q" .) }}
{{- end }}
{{- if $.Values.image.tag }}
{{- printf "%s:%s@%s" $repo $.Values.image.tag . }}
{{- else }}
{{- printf "%s@%s" $repo . }}
{{- end }}
{{- else }}
{{- printf "%s:%s" $repo (.Values.image.tag | default .Chart.AppVersion) }}
{{- end }}
{{- end }}

{{- define "kubeshrink.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kubeshrink.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
