package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/vektah/gqlparser/v2/ast"

	codegenintrospection "github.com/dagger/dagger/cmd/codegen/introspection"
)

// sourceModuleDirectiveName is the directive stamped on every type and
// constructor field a module contributes to a schema, recording which module
// owns it. It mirrors the @sourceModuleName directive carried by the
// introspection JSON the engine hands to SDKs.
const sourceModuleDirectiveName = "sourceModuleName"

// Schema is a manipulable, in-memory GraphQL introspection schema. It wraps
// the introspection JSON shape (cmd/codegen/introspection) shared by every SDK
// so that schema inspection and merge operations can be exposed as engine
// functions, letting all SDKs reuse the exact same implementation.
type Schema struct {
	// Introspection is the parsed introspection response this schema wraps.
	Introspection *codegenintrospection.Response
}

func (*Schema) Type() *ast.Type {
	return &ast.Type{
		NamedType: "Schema",
		NonNull:   true,
	}
}

func (*Schema) TypeDescription() string {
	return "A GraphQL introspection schema that can be inspected and merged."
}

// NewSchema parses introspection JSON into a Schema.
func NewSchema(data JSON) (*Schema, error) {
	resp, err := parseIntrospectionResponse(data)
	if err != nil {
		return nil, fmt.Errorf("parse introspection JSON: %w", err)
	}
	return &Schema{Introspection: resp}, nil
}

// parseIntrospectionResponse decodes introspection JSON and verifies it
// carries a __schema field.
func parseIntrospectionResponse(data JSON) (*codegenintrospection.Response, error) {
	var resp codegenintrospection.Response
	if err := json.Unmarshal(data.Bytes(), &resp); err != nil {
		return nil, err
	}
	if resp.Schema == nil {
		return nil, fmt.Errorf("introspection JSON has no __schema field")
	}
	return &resp, nil
}

// Contents serializes the schema back to introspection JSON.
func (s *Schema) Contents() (JSON, error) {
	data, err := json.Marshal(s.Introspection)
	if err != nil {
		return nil, fmt.Errorf("marshal introspection JSON: %w", err)
	}
	return JSON(data), nil
}

// ListTypes returns the names of the types in the schema. An empty kind
// matches every type; otherwise only types of that kind (OBJECT, INTERFACE,
// ENUM, SCALAR, INPUT_OBJECT, ...) are returned.
func (s *Schema) ListTypes(kind string) []string {
	out := []string{}
	for _, t := range s.Introspection.Schema.Types {
		if kind != "" && string(t.Kind) != kind {
			continue
		}
		out = append(out, t.Name)
	}
	return out
}

// HasType reports whether a type with the given name exists in the schema.
func (s *Schema) HasType(name string) bool {
	return s.Introspection.Schema.Types.Get(name) != nil
}

// DescribeType returns the introspection type with the given name, or nil if
// the schema has no such type.
func (s *Schema) DescribeType(name string) *codegenintrospection.Type {
	return s.Introspection.Schema.Types.Get(name)
}

// Merge returns a new Schema with the module-defined types from moduleTypes
// (itself introspection JSON) appended. Every inserted type, and the module's
// Query constructor field, is stamped with an @sourceModuleName directive
// carrying moduleName.
//
// Merge is idempotent: re-merging a module already present on the schema is a
// no-op (the multi-pass codegen loop reuses the same schema across passes). A
// genuine name collision with a pre-existing, differently-owned type is an
// error. Neither the receiver nor the moduleTypes input is mutated.
func (s *Schema) Merge(moduleTypes JSON, moduleName string) (*Schema, error) {
	if moduleName == "" {
		return nil, fmt.Errorf("module name is required")
	}
	// moduleTypes is parsed into a response Merge solely owns, so stamping
	// directives onto its types never escapes back to the caller.
	module, err := parseIntrospectionResponse(moduleTypes)
	if err != nil {
		return nil, fmt.Errorf("parse module types JSON: %w", err)
	}

	merged, err := cloneResponse(s.Introspection)
	if err != nil {
		return nil, err
	}
	target := merged.Schema

	if moduleAlreadyMerged(target, moduleName) {
		return &Schema{Introspection: merged}, nil
	}

	for _, t := range module.Schema.Types {
		if !isModuleDefinedType(t) {
			continue
		}
		if target.Types.Get(t.Name) != nil {
			return nil, fmt.Errorf("type %q already exists in schema", t.Name)
		}
		t.Directives = append(t.Directives, sourceModuleDirective(moduleName), sourceMapDirective(moduleName))
		target.Types = append(target.Types, t)
	}

	if err := mergeQueryConstructor(target, module.Schema, moduleName); err != nil {
		return nil, err
	}
	return &Schema{Introspection: merged}, nil
}

// isModuleDefinedType reports whether t is a type a module can contribute to a
// schema: a named object, interface or enum that is not a root operation type
// or a built-in introspection type.
func isModuleDefinedType(t *codegenintrospection.Type) bool {
	switch t.Kind {
	case codegenintrospection.TypeKindObject,
		codegenintrospection.TypeKindInterface,
		codegenintrospection.TypeKindEnum:
	default:
		return false
	}
	if strings.HasPrefix(t.Name, "__") {
		return false
	}
	switch t.Name {
	case "Query", "Mutation", "Subscription":
		return false
	}
	return true
}

// mergeQueryConstructor adds the module's constructor field to the schema's
// Query type. If the module's own introspection already declares the field on
// its Query type, that field (carrying its arguments) is reused; otherwise a
// no-arg constructor pointing at the module's main object is synthesized. The
// main object is the one whose name matches moduleName in PascalCase.
func mergeQueryConstructor(target, module *codegenintrospection.Schema, moduleName string) error {
	queryType := target.Query()
	if queryType == nil {
		return fmt.Errorf("schema has no Query type")
	}

	fieldName := strcase.ToLowerCamel(moduleName)
	if findField(queryType, fieldName) != nil {
		// Idempotent: the constructor is already registered.
		return nil
	}

	if modQuery := module.Query(); modQuery != nil {
		if field := findField(modQuery, fieldName); field != nil {
			field.Directives = append(field.Directives, sourceModuleDirective(moduleName), sourceMapDirective(moduleName))
			queryType.Fields = append(queryType.Fields, field)
			return nil
		}
	}

	mainObject := target.Types.Get(strcase.ToCamel(moduleName))
	if mainObject == nil {
		// No main object: the module's other types are still merged, but
		// there is nothing to construct.
		return nil
	}
	queryType.Fields = append(queryType.Fields, &codegenintrospection.Field{
		Name:        fieldName,
		Description: mainObject.Description,
		TypeRef: &codegenintrospection.TypeRef{
			Kind: codegenintrospection.TypeKindNonNull,
			OfType: &codegenintrospection.TypeRef{
				Kind: codegenintrospection.TypeKindObject,
				Name: mainObject.Name,
			},
		},
		Args:       codegenintrospection.InputValues{},
		Directives: codegenintrospection.Directives{sourceModuleDirective(moduleName), sourceMapDirective(moduleName)},
	})
	return nil
}

// moduleAlreadyMerged reports whether the schema already carries a type or a
// Query constructor field stamped with @sourceModuleName for the given module.
func moduleAlreadyMerged(schema *codegenintrospection.Schema, moduleName string) bool {
	want := encodeDirectiveValue(moduleName)
	for _, t := range schema.Types {
		if hasSourceModuleDirective(t.Directives, want) {
			return true
		}
	}
	if query := schema.Query(); query != nil {
		for _, f := range query.Fields {
			if hasSourceModuleDirective(f.Directives, want) {
				return true
			}
		}
	}
	return false
}

// hasSourceModuleDirective reports whether directives contain an
// @sourceModuleName whose name argument equals the encoded value.
func hasSourceModuleDirective(directives codegenintrospection.Directives, encodedName string) bool {
	d := directives.Directive(sourceModuleDirectiveName)
	if d == nil {
		return false
	}
	v := d.Arg("name")
	return v != nil && *v == encodedName
}

// sourceModuleDirective builds the @sourceModuleName directive stamped on
// merged types and constructor fields.
func sourceModuleDirective(moduleName string) *codegenintrospection.Directive {
	value := encodeDirectiveValue(moduleName)
	return &codegenintrospection.Directive{
		Name: sourceModuleDirectiveName,
		Args: []*codegenintrospection.DirectiveArg{
			{Name: "name", Value: &value},
		},
	}
}

// sourceMapDirective builds the @sourceMap directive stamped on merged
// types and the constructor field. The codegen file-splitter
// (cmd/codegen/introspection: DependencyNames/Include/Exclude) reads the
// "module" arg to place a module's types in internal/dagger/<module>.gen.go.
func sourceMapDirective(moduleName string) *codegenintrospection.Directive {
	value := encodeDirectiveValue(moduleName)
	return &codegenintrospection.Directive{
		Name: "sourceMap",
		Args: []*codegenintrospection.DirectiveArg{
			{Name: "module", Value: &value},
		},
	}
}

// encodeDirectiveValue JSON-encodes a directive argument value, mirroring how
// introspection responses carry directive argument values (a string is
// quoted). The write side (sourceModuleDirective) and the read side
// (moduleAlreadyMerged) share this so the encoding can never drift.
func encodeDirectiveValue(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

func findField(t *codegenintrospection.Type, name string) *codegenintrospection.Field {
	for _, f := range t.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// cloneResponse deep-copies an introspection response through a JSON round
// trip, so Merge can mutate the copy without affecting the receiver.
func cloneResponse(r *codegenintrospection.Response) (*codegenintrospection.Response, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("clone schema: %w", err)
	}
	var out codegenintrospection.Response
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("clone schema: %w", err)
	}
	return &out, nil
}

// The IntrospectionType graph below exposes the introspection JSON types
// (cmd/codegen/introspection) as a queryable dagql object graph. Each wrapper
// holds a pointer to the underlying introspection definition; the resolvers
// that read them live in core/schema/schematool.go.

// IntrospectionType is a type defined in a GraphQL introspection schema.
type IntrospectionType struct {
	Def *codegenintrospection.Type
}

func (*IntrospectionType) Type() *ast.Type {
	return &ast.Type{NamedType: "IntrospectionType", NonNull: true}
}

func (*IntrospectionType) TypeDescription() string {
	return "A type defined in a GraphQL introspection schema."
}

// IntrospectionField is a field of an introspection object or interface type.
type IntrospectionField struct {
	Def *codegenintrospection.Field
}

func (*IntrospectionField) Type() *ast.Type {
	return &ast.Type{NamedType: "IntrospectionField", NonNull: true}
}

func (*IntrospectionField) TypeDescription() string {
	return "A field of a GraphQL introspection object or interface type."
}

// IntrospectionTypeRef is a (possibly nested) reference to an introspection
// type, used for field types, argument types and list/non-null wrappers.
type IntrospectionTypeRef struct {
	Def *codegenintrospection.TypeRef
}

func (*IntrospectionTypeRef) Type() *ast.Type {
	return &ast.Type{NamedType: "IntrospectionTypeRef", NonNull: true}
}

func (*IntrospectionTypeRef) TypeDescription() string {
	return "A reference to a GraphQL introspection type."
}

// IntrospectionInputValue is an introspection input field or function
// argument.
type IntrospectionInputValue struct {
	Def *codegenintrospection.InputValue
}

func (*IntrospectionInputValue) Type() *ast.Type {
	return &ast.Type{NamedType: "IntrospectionInputValue", NonNull: true}
}

func (*IntrospectionInputValue) TypeDescription() string {
	return "A GraphQL introspection input field or argument."
}

// IntrospectionEnumValue is a possible value of an introspection enum type.
type IntrospectionEnumValue struct {
	Def *codegenintrospection.EnumValue
}

func (*IntrospectionEnumValue) Type() *ast.Type {
	return &ast.Type{NamedType: "IntrospectionEnumValue", NonNull: true}
}

func (*IntrospectionEnumValue) TypeDescription() string {
	return "A possible value of a GraphQL introspection enum type."
}

// IntrospectionDirective is a directive applied to an introspection type,
// field, argument or enum value.
type IntrospectionDirective struct {
	Def *codegenintrospection.Directive
}

func (*IntrospectionDirective) Type() *ast.Type {
	return &ast.Type{NamedType: "IntrospectionDirective", NonNull: true}
}

func (*IntrospectionDirective) TypeDescription() string {
	return "A directive applied to a GraphQL introspection element."
}

// IntrospectionDirectiveArg is an argument of an applied introspection
// directive.
type IntrospectionDirectiveArg struct {
	Def *codegenintrospection.DirectiveArg
}

func (*IntrospectionDirectiveArg) Type() *ast.Type {
	return &ast.Type{NamedType: "IntrospectionDirectiveArg", NonNull: true}
}

func (*IntrospectionDirectiveArg) TypeDescription() string {
	return "An argument of an applied GraphQL introspection directive."
}
