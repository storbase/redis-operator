{{- define "redis-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "redis-operator.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "redis-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" -}}
{{- end -}}

{{- define "redis-operator.labels" -}}
helm.sh/chart: {{ include "redis-operator.chart" . }}
app.kubernetes.io/name: {{ include "redis-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "redis-operator.selectorLabels" -}}
control-plane: controller-manager
app.kubernetes.io/name: {{ include "redis-operator.name" . }}
{{- end -}}

{{- define "redis-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (printf "%s-controller-manager" (include "redis-operator.fullname" .)) .Values.serviceAccount.name -}}
{{- else -}}
{{- required "serviceAccount.name must be set when serviceAccount.create=false" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
