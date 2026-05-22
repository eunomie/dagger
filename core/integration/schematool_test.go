package core

// These tests exercise the engine's schemaTools surface (the `schema(json)`
// constructor plus the Schema object's merge/inspect operations and the
// IntrospectionType object graph) via raw GraphQL. The SDK does not yet expose
// typed bindings for these fields, so each test issues GraphQL directly.

import (
	"context"
	"testing"

	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"

	"github.com/dagger/dagger/internal/testutil"
)

type SchemaToolsSuite struct{}

func TestSchemaTools(t *testing.T) {
	testctx.New(t, Middleware()...).RunTests(SchemaToolsSuite{})
}

// baseSchemaToolsJSON is the introspection schema used as the merge target.
// It is a minimal valid response with a Query root and one pre-existing
// object.
const baseSchemaToolsJSON = `{
  "__schema": {
    "queryType": {"name": "Query"},
    "types": [
      {"kind":"OBJECT","name":"Query","fields":[],"interfaces":[],"directives":[]},
      {"kind":"OBJECT","name":"Container","fields":[],"interfaces":[],"directives":[]}
    ],
    "directives": []
  },
  "__schemaVersion": "test"
}`

// echoSchemaToolsModuleJSON declares a single object with no Query type, so
// merge must synthesize a no-arg constructor field on Query.
const echoSchemaToolsModuleJSON = `{
  "__schema": {
    "types": [
      {"kind":"OBJECT","name":"Echo","description":"Echo object","fields":[
        {"name":"say","description":"Say something",
         "type":{"kind":"NON_NULL","ofType":{"kind":"SCALAR","name":"String"}},
         "args":[],"directives":[]}
      ],"interfaces":[],"directives":[]}
    ],
    "directives": []
  }
}`

func (SchemaToolsSuite) TestInspect(ctx context.Context, t *testctx.T) {
	res, err := testutil.Query[struct {
		Schema struct {
			Types        []string `json:"types"`
			Objects      []string `json:"objects"`
			HasContainer bool     `json:"hasContainer"`
			HasGhost     bool     `json:"hasGhost"`
			Container    *struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"container"`
			Missing *struct {
				Name string `json:"name"`
			} `json:"missing"`
		} `json:"schema"`
	}](t, `query Inspect($json: JSON!) {
		schema(json: $json) {
			types: listTypes
			objects: listTypes(kind: "OBJECT")
			hasContainer: hasType(name: "Container")
			hasGhost: hasType(name: "Ghost")
			container: describeType(name: "Container") { kind name }
			missing: describeType(name: "Ghost") { name }
		}
	}`, &testutil.QueryOptions{Variables: map[string]any{"json": baseSchemaToolsJSON}})
	require.NoError(t, err)

	require.ElementsMatch(t, []string{"Query", "Container"}, res.Schema.Objects)
	require.Contains(t, res.Schema.Types, "Query")
	require.Contains(t, res.Schema.Types, "Container")
	require.True(t, res.Schema.HasContainer)
	require.False(t, res.Schema.HasGhost)
	require.NotNil(t, res.Schema.Container)
	require.Equal(t, "OBJECT", res.Schema.Container.Kind)
	require.Equal(t, "Container", res.Schema.Container.Name)
	require.Nil(t, res.Schema.Missing)
}

func (SchemaToolsSuite) TestMerge(ctx context.Context, t *testctx.T) {
	type directive struct {
		Name string `json:"name"`
		Args []struct {
			Name  string  `json:"name"`
			Value *string `json:"value"`
		} `json:"args"`
	}
	type field struct {
		Name string `json:"name"`
		Type struct {
			Kind   string `json:"kind"`
			OfType *struct {
				Kind string  `json:"kind"`
				Name *string `json:"name"`
			} `json:"ofType"`
		} `json:"type"`
		Directives []directive `json:"directives"`
	}
	hasSourceModuleStamp := func(directives []directive, encodedName string) bool {
		for _, d := range directives {
			if d.Name != "sourceModuleName" {
				continue
			}
			for _, a := range d.Args {
				if a.Name == "name" && a.Value != nil && *a.Value == encodedName {
					return true
				}
			}
		}
		return false
	}

	res, err := testutil.Query[struct {
		Schema struct {
			Merged struct {
				HasEcho bool     `json:"hasEcho"`
				Types   []string `json:"types"`
				Echo    *struct {
					Kind        string      `json:"kind"`
					Name        string      `json:"name"`
					Description string      `json:"description"`
					Directives  []directive `json:"directives"`
				} `json:"echo"`
				Query *struct {
					Fields []field `json:"fields"`
				} `json:"query"`
			} `json:"merged"`
		} `json:"schema"`
	}](t, `query Merge($base: JSON!, $module: JSON!) {
		schema(json: $base) {
			merged: merge(moduleTypes: $module, moduleName: "echo") {
				hasEcho: hasType(name: "Echo")
				types: listTypes(kind: "OBJECT")
				echo: describeType(name: "Echo") {
					kind name description
					directives { name args { name value } }
				}
				query: describeType(name: "Query") {
					fields {
						name
						type { kind ofType { kind name } }
						directives { name args { name value } }
					}
				}
			}
		}
	}`, &testutil.QueryOptions{Variables: map[string]any{
		"base":   baseSchemaToolsJSON,
		"module": echoSchemaToolsModuleJSON,
	}})
	require.NoError(t, err)

	merged := res.Schema.Merged
	require.True(t, merged.HasEcho)
	require.ElementsMatch(t, []string{"Query", "Container", "Echo"}, merged.Types)

	require.NotNil(t, merged.Echo)
	require.Equal(t, "OBJECT", merged.Echo.Kind)
	require.Equal(t, "Echo", merged.Echo.Name)
	require.Equal(t, "Echo object", merged.Echo.Description)
	require.True(t, hasSourceModuleStamp(merged.Echo.Directives, `"echo"`),
		"Echo type should carry @sourceModuleName")

	require.NotNil(t, merged.Query)
	var ctor *field
	for i, f := range merged.Query.Fields {
		if f.Name == "echo" {
			ctor = &merged.Query.Fields[i]
			break
		}
	}
	require.NotNil(t, ctor, "Query should have an echo constructor field")
	require.Equal(t, "NON_NULL", ctor.Type.Kind)
	require.NotNil(t, ctor.Type.OfType)
	require.Equal(t, "OBJECT", ctor.Type.OfType.Kind)
	require.NotNil(t, ctor.Type.OfType.Name)
	require.Equal(t, "Echo", *ctor.Type.OfType.Name)
	require.True(t, hasSourceModuleStamp(ctor.Directives, `"echo"`),
		"echo constructor should carry @sourceModuleName")
}

func (SchemaToolsSuite) TestMergeIdempotent(ctx context.Context, t *testctx.T) {
	res, err := testutil.Query[struct {
		Schema struct {
			Once struct {
				Again struct {
					HasEcho bool `json:"hasEcho"`
					Query   struct {
						Fields []struct {
							Name string `json:"name"`
						} `json:"fields"`
					} `json:"query"`
				} `json:"again"`
			} `json:"once"`
		} `json:"schema"`
	}](t, `query MergeTwice($base: JSON!, $module: JSON!) {
		schema(json: $base) {
			once: merge(moduleTypes: $module, moduleName: "echo") {
				again: merge(moduleTypes: $module, moduleName: "echo") {
					hasEcho: hasType(name: "Echo")
					query: describeType(name: "Query") { fields { name } }
				}
			}
		}
	}`, &testutil.QueryOptions{Variables: map[string]any{
		"base":   baseSchemaToolsJSON,
		"module": echoSchemaToolsModuleJSON,
	}})
	require.NoError(t, err)
	require.True(t, res.Schema.Once.Again.HasEcho)

	var echoCount int
	for _, f := range res.Schema.Once.Again.Query.Fields {
		if f.Name == "echo" {
			echoCount++
		}
	}
	require.Equal(t, 1, echoCount, "re-merging the same module must not duplicate the constructor")
}

func (SchemaToolsSuite) TestMergeConflict(ctx context.Context, t *testctx.T) {
	const conflict = `{"__schema":{"types":[{"kind":"OBJECT","name":"Container","fields":[],"interfaces":[],"directives":[]}],"directives":[]}}`
	_, err := testutil.Query[struct{}](t, `query Conflict($base: JSON!, $module: JSON!) {
		schema(json: $base) {
			merge(moduleTypes: $module, moduleName: "conflicting") {
				hasType(name: "Container")
			}
		}
	}`, &testutil.QueryOptions{Variables: map[string]any{
		"base":   baseSchemaToolsJSON,
		"module": conflict,
	}})
	require.ErrorContains(t, err, "already exists")
}

func (SchemaToolsSuite) TestContentsRoundTrip(ctx context.Context, t *testctx.T) {
	res, err := testutil.Query[struct {
		Schema struct {
			Contents string `json:"contents"`
		} `json:"schema"`
	}](t, `query Contents($json: JSON!) { schema(json: $json) { contents } }`,
		&testutil.QueryOptions{Variables: map[string]any{"json": baseSchemaToolsJSON}})
	require.NoError(t, err)
	require.Contains(t, res.Schema.Contents, "Container")

	// Round-trip the serialized JSON back into the engine and verify the
	// types are preserved.
	back, err := testutil.Query[struct {
		Schema struct {
			Types []string `json:"types"`
		} `json:"schema"`
	}](t, `query RoundTrip($json: JSON!) { schema(json: $json) { types: listTypes } }`,
		&testutil.QueryOptions{Variables: map[string]any{"json": res.Schema.Contents}})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"Query", "Container"}, back.Schema.Types)
}

func (SchemaToolsSuite) TestIntrospectionGraph(ctx context.Context, t *testctx.T) {
	res, err := testutil.Query[struct {
		Schema struct {
			Merged struct {
				Echo *struct {
					Kind   string `json:"kind"`
					Fields *[]struct {
						Name string `json:"name"`
						Type struct {
							Kind   string  `json:"kind"`
							Name   *string `json:"name"`
							OfType *struct {
								Kind string  `json:"kind"`
								Name *string `json:"name"`
							} `json:"ofType"`
						} `json:"type"`
					} `json:"fields"`
					InputFields *[]struct {
						Name string `json:"name"`
					} `json:"inputFields"`
					EnumValues *[]struct {
						Name string `json:"name"`
					} `json:"enumValues"`
				} `json:"echo"`
			} `json:"merged"`
		} `json:"schema"`
	}](t, `query Graph($base: JSON!, $module: JSON!) {
		schema(json: $base) {
			merged: merge(moduleTypes: $module, moduleName: "echo") {
				echo: describeType(name: "Echo") {
					kind
					fields { name type { kind name ofType { kind name } } }
					inputFields { name }
					enumValues { name }
				}
			}
		}
	}`, &testutil.QueryOptions{Variables: map[string]any{
		"base":   baseSchemaToolsJSON,
		"module": echoSchemaToolsModuleJSON,
	}})
	require.NoError(t, err)

	echo := res.Schema.Merged.Echo
	require.NotNil(t, echo)
	require.Equal(t, "OBJECT", echo.Kind)
	require.NotNil(t, echo.Fields, "OBJECT type's fields must not be null")
	require.Len(t, *echo.Fields, 1)
	say := (*echo.Fields)[0]
	require.Equal(t, "say", say.Name)
	require.Equal(t, "NON_NULL", say.Type.Kind)
	require.Nil(t, say.Type.Name)
	require.NotNil(t, say.Type.OfType)
	require.Equal(t, "SCALAR", say.Type.OfType.Kind)
	require.NotNil(t, say.Type.OfType.Name)
	require.Equal(t, "String", *say.Type.OfType.Name)
	require.Nil(t, echo.InputFields, "OBJECT type's inputFields must be null")
	require.Nil(t, echo.EnumValues, "OBJECT type's enumValues must be null")
}

func (SchemaToolsSuite) TestLiveEngineSchema(ctx context.Context, t *testctx.T) {
	// Fetch the engine's own introspection JSON and round-trip it through
	// schemaTools. The Schema type itself must be present in the result, a
	// self-referential proof that this feature is installed correctly.
	live, err := testutil.Query[struct {
		File struct {
			Contents string `json:"contents"`
		} `json:"__schemaJSONFile"`
	}](t, `query LiveSchema { __schemaJSONFile { contents } }`, nil)
	require.NoError(t, err)
	require.NotEmpty(t, live.File.Contents)

	res, err := testutil.Query[struct {
		Schema struct {
			HasContainer bool `json:"hasContainer"`
			HasFile      bool `json:"hasFile"`
			HasSchema    bool `json:"hasSchema"`
		} `json:"schema"`
	}](t, `query Live($json: JSON!) {
		schema(json: $json) {
			hasContainer: hasType(name: "Container")
			hasFile: hasType(name: "File")
			hasSchema: hasType(name: "Schema")
		}
	}`, &testutil.QueryOptions{Variables: map[string]any{"json": live.File.Contents}})
	require.NoError(t, err)
	require.True(t, res.Schema.HasContainer)
	require.True(t, res.Schema.HasFile)
	require.True(t, res.Schema.HasSchema, "the new Schema type should be present in the live engine schema")
}
