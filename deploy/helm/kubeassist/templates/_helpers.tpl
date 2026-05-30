{{- define "kubeassist.fullname" -}}
kubeassist
{{- end -}}

{{- define "kubeassist.labels" -}}
app.kubernetes.io/name: kubeassist
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
