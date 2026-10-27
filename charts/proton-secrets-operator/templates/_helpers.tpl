{{- define "pso.name" -}}{{ .Chart.Name }}{{- end }}
{{- define "pso.fullname" -}}
{{- if contains .Chart.Name .Release.Name }}{{ .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}{{ printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}{{- end }}
{{- end }}
{{- define "pso.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "pso.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/part-of: proton-secrets-operator
{{- end }}
{{- define "pso.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pso.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "pso.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}{{ default (include "pso.fullname" .) .Values.serviceAccount.name }}{{ else }}{{ default "default" .Values.serviceAccount.name }}{{ end }}
{{- end }}
{{- define "pso.image" -}}
{{- if .Values.image.digest }}{{ printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else }}{{ printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}{{ end }}
{{- end }}
{{- define "pso.passCliImage" -}}
{{- with .Values.passCli.image }}{{ if .digest }}{{ printf "%s@%s" .repository .digest }}{{ else }}{{ printf "%s:%s" .repository (required "passCli.image.tag or digest is required when passCli.image.repository is set" .tag) }}{{ end }}{{ end }}
{{- end }}
