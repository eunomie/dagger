package core

import (
	"context"

	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"
)

// TestPythonStaticEntrypointBasic verifies end-to-end that a Python
// module goes through the new static entrypoint pipeline: codegen
// emits _dagger_main.py, the runtime imports it, and a basic function
// call succeeds.
func (ModuleSuite) TestPythonStaticEntrypointBasic(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type

@object_type
class Test:
    @function
    def hello(self) -> str:
        return "hi"
`
	modGen := modInit(t, c, "python", source)

	out, err := modGen.
		With(daggerQuery(`{hello}`)).
		Stdout(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"hello":"hi"}`, out)
}

// TestPythonStaticEntrypointCustomObject verifies that a user
// @object_type with a returned instance + chained method call works
// (exercises parent-state rehydrate_parent() + the static dispatch for
// custom types).
func (ModuleSuite) TestPythonStaticEntrypointCustomObject(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type
import dataclasses

@object_type
@dataclasses.dataclass
class Greeter:
    tone: str = "cheerful"

    @function
    def greet(self, name: str) -> str:
        return f"{self.tone} hello, {name}"

@object_type
class Test:
    @function
    def new_greeter(self, tone: str = "cheerful") -> Greeter:
        return Greeter(tone=tone)
`
	modGen := modInit(t, c, "python", source)

	out, err := modGen.
		With(daggerQuery(`{newGreeter(tone:"warm"){greet(name:"world")}}`)).
		Stdout(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"newGreeter":{"greet":"warm hello, world"}}`, out)
}

// TestPythonStaticEntrypointEnum verifies enum typedef generation and
// dispatch coerce_enum + unstructure_enum helpers.
func (ModuleSuite) TestPythonStaticEntrypointEnum(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import enum
from dagger import function, object_type, enum_type

@enum_type
class Color(enum.Enum):
    RED = "red"
    BLUE = "blue"

@object_type
class Test:
    @function
    def flip(self, color: Color) -> Color:
        return Color.BLUE if color == Color.RED else Color.RED
`
	modGen := modInit(t, c, "python", source)

	out, err := modGen.
		With(daggerQuery(`{flip(color:RED)}`)).
		Stdout(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"flip":"BLUE"}`, out)
}

// TestPythonStaticEntrypointFileShape verifies that the codegen'd
// _dagger_main.py actually lands in the user's source tree and pins
// the zero-runtime-analysis invariants structurally: the expected
// shape (build_module + invoke + match) is present and the banned
// introspection references (__annotations__, __dagger_module__) are
// absent.  The module name "test" in the file path comes from the
// directory name passed to modInit.
func (ModuleSuite) TestPythonStaticEntrypointFileShape(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type

@object_type
class Test:
    @function
    def hello(self) -> str:
        return "hi"
`
	modGen := modInit(t, c, "python", source)

	content, err := modGen.
		File("src/test/_dagger_main.py").
		Contents(ctx)
	require.NoError(t, err)

	require.Contains(t, content, "async def build_module")
	require.Contains(t, content, "async def invoke")
	require.Contains(t, content, "match (parent_name, fn_name):")
	require.Contains(t, content, `case ("Test", "hello"):`)
	require.NotContains(t, content, "__annotations__",
		"generated entrypoint must contain no runtime type introspection")
	require.NotContains(t, content, "__dagger_module__",
		"generated entrypoint must not rely on the dynamic decorator registry")

	// Invariant: user-class imports must be inside each case arm, not
	// at module top. build_module(dag) runs at register time and must
	// not require the user's package to be importable.
	require.NotRegexp(t, `(?m)^from test import`, content,
		"user-class imports must be inside case arms, not at module top")
}

// TestPythonStaticEntrypointList verifies that a function with a
// list-of-primitives parameter round-trips through the generated
// entrypoint (exercises coerce_list in the dispatch path).
func (ModuleSuite) TestPythonStaticEntrypointList(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type

@object_type
class Test:
    @function
    def join(self, parts: list[str]) -> str:
        return ",".join(parts)
`
	modGen := modInit(t, c, "python", source)

	out, err := modGen.
		With(daggerQuery(`{join(parts:["a","b","c"])}`)).
		Stdout(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"join":"a,b,c"}`, out)
}
