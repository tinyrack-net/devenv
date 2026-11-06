{{- define "devenv.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "devenv.fullname" -}}
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

{{- define "devenv.selectorLabels" -}}
app.kubernetes.io/name: {{ include "devenv.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "devenv.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "devenv.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "devenv.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "devenv.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "devenv.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}

{{- define "devenv.authSecretName" -}}
{{- default (printf "%s-auth" (include "devenv.fullname" .)) .Values.auth.existingSecret }}
{{- end }}

{{/* Generated secrets survive upgrades by reading the existing object. */}}
{{- define "devenv.persistedRandom" -}}
{{- $ctx := index . 0 }}{{- $name := index . 1 }}{{- $key := index . 2 }}{{- $len := index . 3 }}
{{- $existing := lookup "v1" "Secret" $ctx.Release.Namespace $name }}
{{- if and $existing (index $existing.data $key) }}
{{- index $existing.data $key }}
{{- else }}
{{- randAlphaNum $len | b64enc }}
{{- end }}
{{- end }}
