{{- /* Types definition generation.
Export a type for each type or input existing in the GraphQL schema.
 */ -}}
{{ define "types" -}}
	{{- range .Types }}
		{{- template "type" . }}
	{{- end }}
{{- end }}

{{ define "type" }}
	{{- /* Generate scalar type. */ -}}
	{{- if IsCustomScalar . }}
		{{- if .Description }}
			{{- /* Split comment string into a slice of one line per element. */ -}}
			{{- $desc := CommentToLines .Description }}
				{{- range $desc }}
  # {{ . }}
				{{- end }}
		{{- end }}
  # export type {{ .Name }} = string & {__{{ .Name }}: never}
{{ "" }}
	{{- end }}
	{{- /* Generate enum */ -}}
	{{- if IsEnum . }}
		{{- if .Description }}
			{{- /* Split comment string into a slice of one line per element. */ -}}
			{{- $desc := CommentToLines .Description }}
				{{- range $desc }}
  # {{ . }}
				{{- end }}
		{{- end }}
  module {{ .Name }}
		{{- $sortedEnumValues := SortEnumFields .EnumValues }}
		{{- range $sortedEnumValues }}
			{{- if .Description }}
				{{- /* Split comment string into a slice of one line per element. */ -}}
				{{- $desc := CommentToLines .Description }}
  				{{- range $desc }}
    # {{ . }}
				{{- end }}
			{{- end }}
    {{ .Name | ToUpperCase | FormatEnum }} = Class.new
		{{- end }}
  end
	{{- end }}
	{{- /* Generate structure type. */ -}}
	{{- with .Fields }}
		{{- range . }}
			{{- $optionals := GetOptionalArgs .Args }}
			{{- if gt (len $optionals) 0 }}
  {{ $.Name | QueryToClient }}{{ .Name | PascalCase }}Opts = Struct.new(
				{{- template "field" $optionals }}
  )
{{ "" }}	{{- end }}
		{{- end }}
	{{- end }}
	{{- /* Generate input GraphQL type. */ -}}
	{{- with .InputFields }}
  {{ $.Name | FormatName }} = Struct.new(
		{{- template "field" (SortInputFields .) }}
  )
{{ "" }}
	{{- end }}
{{- end }}

{{- define "field" }}
	{{- range $i, $field := . }}
		{{- if ne $i 0 }},

		{{- end }}
		{{- /* Write description. */ -}}
		{{- if $field.Description }}
			{{- $desc := CommentToLines $field.Description }}
			{{- range $desc }}
    # {{ . }}
			{{- end }}
		{{- end }}
    :{{ $field.Name }}
	{{- end }}
{{- end }}
