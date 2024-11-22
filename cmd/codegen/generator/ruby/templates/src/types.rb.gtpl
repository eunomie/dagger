{{- /* Types definition generation.
Export a type for each type or input existing in the GraphQL schema.
 */ -}}
{{ define "types" }}
	{{- range .Types }}
		{{- if HasPrefix .Name "_" }}
			{{- /* we ignore types prefixed by _ */ -}}
		{{- else }}
			{{- template "type" . }}
		{{- end }}
	{{- end }}
{{- end }}

{{ define "type" }}
	{{- /* Generate scalar type. */ -}}
	{{- if IsCustomScalar . }}
		{{- if .Description }}
			{{- /* Split comment string into a slice of one line per element. */ -}}
			{{- $desc := CommentToLines .Description }}
			{{- range $desc }}
  #{{ . }}
			{{- end }}
		{{- end }}
  {{ .Name }} = T.type_alias { String }
{{ "" }}
	{{- end }}

	{{- /* Generate enum */ -}}
	{{- if IsEnum . }}
		{{- if .Description }}
			{{- /* Split comment string into a slice of one line per element. */ -}}
			{{- $desc := CommentToLines .Description }}
			{{- range $desc }}
  #{{ . }}
			{{- end }}
		{{- end }}
  class {{ .Name | FormatName }} < T::Enum
    enums do
		{{- $sortedEnumValues := SortEnumFields .EnumValues }}
		{{- range $i, $value := $sortedEnumValues }}
			{{- if $value.Description }}
				{{- /* Split comment string into a slice of one line per element. */ -}}
				{{- $desc := CommentToLines $value.Description }}
				{{- /* Add extra break line if it's not the first param. */ -}}
				{{- if ne $i 0 }}
{{""}}
				{{- end }}
				{{- range $desc }}
      #{{ . }}
				{{- end }}
			{{- end }}
      {{ $value.Name | FormatEnum }} = new
		{{- end }}
    end
  end
{{ "" }}
	{{- end }}

	{{- /* Generate structure type. */ -}}
	{{- with .Fields }}
		{{- range . }}
			{{- $optionals := GetOptionalArgs .Args }}
			{{- if gt (len $optionals) 0 }}
  class {{ $.Name | QueryToClient }}{{ .Name | PascalCase }}Opts < T::Struct
				{{- template "field" $optionals }}
  end
{{ "" }}	{{- end }}
		{{- end }}
	{{- end }}

	{{- /* Generate input GraphQL type. */ -}}
	{{- with .InputFields }}
  class {{ $.Name | FormatName }} < T::Struct
		{{- template "field" (SortInputFields .) }}
  end
{{ "" }}
	{{- end }}

{{- end }}

{{- define "field" }}
	{{- range $i, $field := . }}
		{{- $pre := "" }}
		{{- $post := "" }}
		{{- if $field.TypeRef.IsOptional }}
			{{- $pre = "T.nilable(" }}
			{{- $post = ")" }}
		{{- end }}
		{{- /* Write description. */ -}}
		{{- if $field.Description }}
			{{- $desc := CommentToLines $field.Description }}

			{{- /* Add extra break line if it's not the first param. */ -}}
			{{- if ne $i 0 }}
{{""}}
			{{- end }}
			{{- range $desc }}
    #{{ . }}
			{{- end }}
		{{- end }}
		{{- /* Write type, if it's an id it's an output, otherwise it's an input. */ -}}
		{{- if eq $field.Name "id" }}
    prop :{{ $field.Name | FormatArg }}, {{ $pre }}{{ $field.TypeRef | FormatOutputType }}{{ $post }}
		{{- else }}
    prop :{{ $field.Name | FormatArg }}, {{ $pre }}{{ $field.TypeRef | FormatInputType }}{{ $post }}
		{{- end }}

	{{- end }}
{{- end }}
