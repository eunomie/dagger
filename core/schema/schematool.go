package schema

import (
	"context"
	"fmt"

	codegenintrospection "github.com/dagger/dagger/cmd/codegen/introspection"
	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/dagql"
)

// schemaToolsSchema exposes schema-manipulation tools that operate on GraphQL
// introspection JSON. The tools are provided by the engine so that every SDK
// reasons about schemas through the exact same implementation, rather than
// reimplementing it in each language.
type schemaToolsSchema struct{}

var _ SchemaResolvers = &schemaToolsSchema{}

func (s *schemaToolsSchema) Install(srv *dagql.Server) {
	dagql.Fields[*core.Query]{
		dagql.Func("schema", s.schema).
			Doc(`Load a GraphQL introspection schema for inspection and merging.`).
			Args(
				dagql.Arg("json").Doc(`The introspection schema JSON to load.`),
			),
	}.Install(srv)

	dagql.Fields[*core.Schema]{
		dagql.Func("contents", s.contents).
			Doc(`Serialize the schema back to introspection JSON.`),
		dagql.Func("listTypes", s.listTypes).
			Doc(`List the names of the types defined in the schema.`).
			Args(
				dagql.Arg("kind").Doc(`Only list types of this kind, e.g. "OBJECT", "INTERFACE", "ENUM", "SCALAR" or "INPUT_OBJECT". Lists every type if omitted.`),
			),
		dagql.Func("hasType", s.hasType).
			Doc(`Check whether a type with the given name exists in the schema.`).
			Args(
				dagql.Arg("name").Doc(`The name of the type to look for.`),
			),
		dagql.Func("describeType", s.describeType).
			Doc(`Return the full introspection details of a named type, or null if the schema has no such type.`).
			Args(
				dagql.Arg("name").Doc(`The name of the type to describe.`),
			),
		dagql.Func("merge", s.merge).
			Doc(`Merge a module's introspection-shaped type definitions into the schema, returning the combined schema.`).
			Args(
				dagql.Arg("moduleTypes").Doc(`Introspection JSON describing the types the module defines. Object, interface and enum types are appended to the schema, and a constructor field for the module is added to the Query type.`),
				dagql.Arg("moduleName").Doc(`The name of the module whose types are being merged. Used to stamp the @sourceModuleName directive and to derive the module's constructor field.`),
			),
	}.Install(srv)

	s.installIntrospectionGraph(srv)
}

func (s *schemaToolsSchema) schema(ctx context.Context, _ *core.Query, args struct {
	JSON core.JSON `name:"json"`
}) (*core.Schema, error) {
	return core.NewSchema(args.JSON)
}

func (s *schemaToolsSchema) contents(ctx context.Context, self *core.Schema, _ struct{}) (core.JSON, error) {
	return self.Contents()
}

func (s *schemaToolsSchema) listTypes(ctx context.Context, self *core.Schema, args struct {
	Kind string `default:""`
}) ([]string, error) {
	return self.ListTypes(args.Kind), nil
}

func (s *schemaToolsSchema) hasType(ctx context.Context, self *core.Schema, args struct {
	Name string
}) (bool, error) {
	return self.HasType(args.Name), nil
}

func (s *schemaToolsSchema) describeType(ctx context.Context, self *core.Schema, args struct {
	Name string
}) (dagql.Nullable[*core.IntrospectionType], error) {
	t := self.DescribeType(args.Name)
	if t == nil {
		return dagql.Null[*core.IntrospectionType](), nil
	}
	return dagql.NonNull(&core.IntrospectionType{Def: t}), nil
}

func (s *schemaToolsSchema) merge(ctx context.Context, self *core.Schema, args struct {
	ModuleTypes core.JSON
	ModuleName  string
}) (*core.Schema, error) {
	return self.Merge(args.ModuleTypes, args.ModuleName)
}

// installIntrospectionGraph installs the queryable object graph that
// describeType returns: a faithful projection of the introspection JSON types.
// Like every other core object, these types are ID-able: the engine's EnvHook
// requires an ID type for any object installed after the environment schema.
func (s *schemaToolsSchema) installIntrospectionGraph(srv *dagql.Server) {
	dagql.Fields[*core.IntrospectionType]{
		dagql.Func("kind", func(_ context.Context, self *core.IntrospectionType, _ struct{}) (string, error) {
			return string(self.Def.Kind), nil
		}).Doc(`The kind of the type, e.g. "OBJECT", "INTERFACE", "ENUM" or "SCALAR".`).
			DoNotCache("simple field selection"),
		dagql.Func("name", func(_ context.Context, self *core.IntrospectionType, _ struct{}) (string, error) {
			return self.Def.Name, nil
		}).Doc(`The name of the type.`).
			DoNotCache("simple field selection"),
		dagql.Func("description", func(_ context.Context, self *core.IntrospectionType, _ struct{}) (string, error) {
			return self.Def.Description, nil
		}).Doc(`The description of the type.`).
			DoNotCache("simple field selection"),
		dagql.Func("fields", func(_ context.Context, self *core.IntrospectionType, _ struct{}) (dagql.Nullable[dagql.Array[*core.IntrospectionField]], error) {
			switch self.Def.Kind {
			case codegenintrospection.TypeKindObject, codegenintrospection.TypeKindInterface:
				return dagql.NonNull(wrapIntrospectionFields(self.Def.Fields)), nil
			default:
				return dagql.Null[dagql.Array[*core.IntrospectionField]](), nil
			}
		}).Doc(`The fields of the type. Null unless the type is an object or interface.`).
			DoNotCache("simple field selection"),
		dagql.Func("inputFields", func(_ context.Context, self *core.IntrospectionType, _ struct{}) (dagql.Nullable[dagql.Array[*core.IntrospectionInputValue]], error) {
			if self.Def.Kind != codegenintrospection.TypeKindInputObject {
				return dagql.Null[dagql.Array[*core.IntrospectionInputValue]](), nil
			}
			return dagql.NonNull(wrapIntrospectionInputValues(self.Def.InputFields)), nil
		}).Doc(`The input fields of the type. Null unless the type is an input object.`).
			DoNotCache("simple field selection"),
		dagql.Func("enumValues", func(_ context.Context, self *core.IntrospectionType, _ struct{}) (dagql.Nullable[dagql.Array[*core.IntrospectionEnumValue]], error) {
			if self.Def.Kind != codegenintrospection.TypeKindEnum {
				return dagql.Null[dagql.Array[*core.IntrospectionEnumValue]](), nil
			}
			return dagql.NonNull(wrapIntrospectionEnumValues(self.Def.EnumValues)), nil
		}).Doc(`The possible values of the type. Null unless the type is an enum.`).
			DoNotCache("simple field selection"),
		dagql.Func("interfaces", func(_ context.Context, self *core.IntrospectionType, _ struct{}) (dagql.Nullable[dagql.Array[*core.IntrospectionType]], error) {
			switch self.Def.Kind {
			case codegenintrospection.TypeKindObject, codegenintrospection.TypeKindInterface:
				return dagql.NonNull(wrapIntrospectionTypes(self.Def.Interfaces)), nil
			default:
				return dagql.Null[dagql.Array[*core.IntrospectionType]](), nil
			}
		}).Doc(`The interfaces implemented by the type. Null unless the type is an object or interface.`).
			DoNotCache("simple field selection"),
		dagql.Func("directives", func(_ context.Context, self *core.IntrospectionType, _ struct{}) (dagql.Array[*core.IntrospectionDirective], error) {
			return wrapIntrospectionDirectives(self.Def.Directives), nil
		}).Doc(`The directives applied to the type, including @sourceModuleName for module-defined types.`).
			DoNotCache("simple field selection"),
	}.Install(srv)

	dagql.Fields[*core.IntrospectionField]{
		dagql.Func("name", func(_ context.Context, self *core.IntrospectionField, _ struct{}) (string, error) {
			return self.Def.Name, nil
		}).Doc(`The name of the field.`).
			DoNotCache("simple field selection"),
		dagql.Func("description", func(_ context.Context, self *core.IntrospectionField, _ struct{}) (string, error) {
			return self.Def.Description, nil
		}).Doc(`The description of the field.`).
			DoNotCache("simple field selection"),
		dagql.Func("type", func(_ context.Context, self *core.IntrospectionField, _ struct{}) (*core.IntrospectionTypeRef, error) {
			if self.Def.TypeRef == nil {
				return nil, fmt.Errorf("field %q has no type", self.Def.Name)
			}
			return &core.IntrospectionTypeRef{Def: self.Def.TypeRef}, nil
		}).Doc(`The type of the field.`).
			DoNotCache("simple field selection"),
		dagql.Func("args", func(_ context.Context, self *core.IntrospectionField, _ struct{}) (dagql.Array[*core.IntrospectionInputValue], error) {
			return wrapIntrospectionInputValues(self.Def.Args), nil
		}).Doc(`The arguments accepted by the field.`).
			DoNotCache("simple field selection"),
		dagql.Func("isDeprecated", func(_ context.Context, self *core.IntrospectionField, _ struct{}) (bool, error) {
			return self.Def.IsDeprecated, nil
		}).Doc(`Whether the field is deprecated.`).
			DoNotCache("simple field selection"),
		dagql.Func("deprecationReason", func(_ context.Context, self *core.IntrospectionField, _ struct{}) (*string, error) {
			return self.Def.DeprecationReason, nil
		}).Doc(`Why the field is deprecated, if it is.`).
			DoNotCache("simple field selection"),
		dagql.Func("directives", func(_ context.Context, self *core.IntrospectionField, _ struct{}) (dagql.Array[*core.IntrospectionDirective], error) {
			return wrapIntrospectionDirectives(self.Def.Directives), nil
		}).Doc(`The directives applied to the field.`).
			DoNotCache("simple field selection"),
	}.Install(srv)

	dagql.Fields[*core.IntrospectionTypeRef]{
		dagql.Func("kind", func(_ context.Context, self *core.IntrospectionTypeRef, _ struct{}) (string, error) {
			return string(self.Def.Kind), nil
		}).Doc(`The kind of the referenced type, e.g. "OBJECT", "LIST" or "NON_NULL".`).
			DoNotCache("simple field selection"),
		dagql.Func("name", func(_ context.Context, self *core.IntrospectionTypeRef, _ struct{}) (dagql.Nullable[dagql.String], error) {
			if self.Def.Name == "" {
				return dagql.Null[dagql.String](), nil
			}
			return dagql.NonNull(dagql.NewString(self.Def.Name)), nil
		}).Doc(`The name of the referenced type. Null for list and non-null wrappers.`).
			DoNotCache("simple field selection"),
		dagql.Func("ofType", func(_ context.Context, self *core.IntrospectionTypeRef, _ struct{}) (dagql.Nullable[*core.IntrospectionTypeRef], error) {
			if self.Def.OfType == nil {
				return dagql.Null[*core.IntrospectionTypeRef](), nil
			}
			return dagql.NonNull(&core.IntrospectionTypeRef{Def: self.Def.OfType}), nil
		}).Doc(`The type wrapped by a list or non-null reference. Null for named types.`).
			DoNotCache("simple field selection"),
	}.Install(srv)

	dagql.Fields[*core.IntrospectionInputValue]{
		dagql.Func("name", func(_ context.Context, self *core.IntrospectionInputValue, _ struct{}) (string, error) {
			return self.Def.Name, nil
		}).Doc(`The name of the input value.`).
			DoNotCache("simple field selection"),
		dagql.Func("description", func(_ context.Context, self *core.IntrospectionInputValue, _ struct{}) (string, error) {
			return self.Def.Description, nil
		}).Doc(`The description of the input value.`).
			DoNotCache("simple field selection"),
		dagql.Func("defaultValue", func(_ context.Context, self *core.IntrospectionInputValue, _ struct{}) (*string, error) {
			return self.Def.DefaultValue, nil
		}).Doc(`The default value of the input value, encoded as GraphQL, if any.`).
			DoNotCache("simple field selection"),
		dagql.Func("type", func(_ context.Context, self *core.IntrospectionInputValue, _ struct{}) (*core.IntrospectionTypeRef, error) {
			if self.Def.TypeRef == nil {
				return nil, fmt.Errorf("input value %q has no type", self.Def.Name)
			}
			return &core.IntrospectionTypeRef{Def: self.Def.TypeRef}, nil
		}).Doc(`The type of the input value.`).
			DoNotCache("simple field selection"),
		dagql.Func("isDeprecated", func(_ context.Context, self *core.IntrospectionInputValue, _ struct{}) (bool, error) {
			return self.Def.IsDeprecated, nil
		}).Doc(`Whether the input value is deprecated.`).
			DoNotCache("simple field selection"),
		dagql.Func("deprecationReason", func(_ context.Context, self *core.IntrospectionInputValue, _ struct{}) (*string, error) {
			return self.Def.DeprecationReason, nil
		}).Doc(`Why the input value is deprecated, if it is.`).
			DoNotCache("simple field selection"),
		dagql.Func("directives", func(_ context.Context, self *core.IntrospectionInputValue, _ struct{}) (dagql.Array[*core.IntrospectionDirective], error) {
			return wrapIntrospectionDirectives(self.Def.Directives), nil
		}).Doc(`The directives applied to the input value.`).
			DoNotCache("simple field selection"),
	}.Install(srv)

	dagql.Fields[*core.IntrospectionEnumValue]{
		dagql.Func("name", func(_ context.Context, self *core.IntrospectionEnumValue, _ struct{}) (string, error) {
			return self.Def.Name, nil
		}).Doc(`The name of the enum value.`).
			DoNotCache("simple field selection"),
		dagql.Func("description", func(_ context.Context, self *core.IntrospectionEnumValue, _ struct{}) (string, error) {
			return self.Def.Description, nil
		}).Doc(`The description of the enum value.`).
			DoNotCache("simple field selection"),
		dagql.Func("isDeprecated", func(_ context.Context, self *core.IntrospectionEnumValue, _ struct{}) (bool, error) {
			return self.Def.IsDeprecated, nil
		}).Doc(`Whether the enum value is deprecated.`).
			DoNotCache("simple field selection"),
		dagql.Func("deprecationReason", func(_ context.Context, self *core.IntrospectionEnumValue, _ struct{}) (*string, error) {
			return self.Def.DeprecationReason, nil
		}).Doc(`Why the enum value is deprecated, if it is.`).
			DoNotCache("simple field selection"),
		dagql.Func("directives", func(_ context.Context, self *core.IntrospectionEnumValue, _ struct{}) (dagql.Array[*core.IntrospectionDirective], error) {
			return wrapIntrospectionDirectives(self.Def.Directives), nil
		}).Doc(`The directives applied to the enum value.`).
			DoNotCache("simple field selection"),
	}.Install(srv)

	dagql.Fields[*core.IntrospectionDirective]{
		dagql.Func("name", func(_ context.Context, self *core.IntrospectionDirective, _ struct{}) (string, error) {
			return self.Def.Name, nil
		}).Doc(`The name of the directive.`).
			DoNotCache("simple field selection"),
		dagql.Func("args", func(_ context.Context, self *core.IntrospectionDirective, _ struct{}) (dagql.Array[*core.IntrospectionDirectiveArg], error) {
			return wrapIntrospectionDirectiveArgs(self.Def.Args), nil
		}).Doc(`The arguments of the applied directive.`).
			DoNotCache("simple field selection"),
	}.Install(srv)

	dagql.Fields[*core.IntrospectionDirectiveArg]{
		dagql.Func("name", func(_ context.Context, self *core.IntrospectionDirectiveArg, _ struct{}) (string, error) {
			return self.Def.Name, nil
		}).Doc(`The name of the directive argument.`).
			DoNotCache("simple field selection"),
		dagql.Func("value", func(_ context.Context, self *core.IntrospectionDirectiveArg, _ struct{}) (*string, error) {
			return self.Def.Value, nil
		}).Doc(`The value of the directive argument, encoded as JSON, if any.`).
			DoNotCache("simple field selection"),
	}.Install(srv)
}

func wrapIntrospectionTypes(types []*codegenintrospection.Type) dagql.Array[*core.IntrospectionType] {
	out := make(dagql.Array[*core.IntrospectionType], 0, len(types))
	for _, t := range types {
		out = append(out, &core.IntrospectionType{Def: t})
	}
	return out
}

func wrapIntrospectionFields(fields []*codegenintrospection.Field) dagql.Array[*core.IntrospectionField] {
	out := make(dagql.Array[*core.IntrospectionField], 0, len(fields))
	for _, f := range fields {
		out = append(out, &core.IntrospectionField{Def: f})
	}
	return out
}

// wrapIntrospectionInputValues wraps a slice of InputValue. InputValue is a
// value type, so each element is copied before its address is taken — the
// wrappers must not alias the caller's backing array.
func wrapIntrospectionInputValues(values []codegenintrospection.InputValue) dagql.Array[*core.IntrospectionInputValue] {
	out := make(dagql.Array[*core.IntrospectionInputValue], 0, len(values))
	for _, v := range values {
		out = append(out, &core.IntrospectionInputValue{Def: &v})
	}
	return out
}

// wrapIntrospectionEnumValues wraps a slice of EnumValue. As with input
// values, each value-typed element is copied before its address is taken.
func wrapIntrospectionEnumValues(values []codegenintrospection.EnumValue) dagql.Array[*core.IntrospectionEnumValue] {
	out := make(dagql.Array[*core.IntrospectionEnumValue], 0, len(values))
	for _, v := range values {
		out = append(out, &core.IntrospectionEnumValue{Def: &v})
	}
	return out
}

func wrapIntrospectionDirectives(directives codegenintrospection.Directives) dagql.Array[*core.IntrospectionDirective] {
	out := make(dagql.Array[*core.IntrospectionDirective], 0, len(directives))
	for _, d := range directives {
		out = append(out, &core.IntrospectionDirective{Def: d})
	}
	return out
}

func wrapIntrospectionDirectiveArgs(args []*codegenintrospection.DirectiveArg) dagql.Array[*core.IntrospectionDirectiveArg] {
	out := make(dagql.Array[*core.IntrospectionDirectiveArg], 0, len(args))
	for _, a := range args {
		out = append(out, &core.IntrospectionDirectiveArg{Def: a})
	}
	return out
}
