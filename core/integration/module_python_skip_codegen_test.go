package core

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"
)

// TestPythonSkipCodegenAtRuntimeDefault verifies that
// `dagger init --sdk=python` writes codegen.legacyCodegenAtRuntime=false
// and codegen.automaticGitignore=false to the generated dagger.json,
// opting new modules into the committed-files skip-codegen path.
func (ModuleSuite) TestPythonSkipCodegenAtRuntimeDefault(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	// modInit does `dagger init --sdk=python`. The resulting dagger.json
	// should have both flags set to false.
	modGen := modInit(t, c, "python", "")

	raw, err := modGen.File("dagger.json").Contents(ctx)
	require.NoError(t, err)

	var cfg struct {
		Codegen *struct {
			AutomaticGitignore     *bool `json:"automaticGitignore"`
			LegacyCodegenAtRuntime *bool `json:"legacyCodegenAtRuntime"`
		} `json:"codegen"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &cfg))
	require.NotNil(t, cfg.Codegen, "dagger.json must contain a codegen section")
	require.NotNil(t, cfg.Codegen.AutomaticGitignore)
	require.False(t, *cfg.Codegen.AutomaticGitignore,
		"automaticGitignore must be explicitly false")
	require.NotNil(t, cfg.Codegen.LegacyCodegenAtRuntime)
	require.False(t, *cfg.Codegen.LegacyCodegenAtRuntime,
		"legacyCodegenAtRuntime must be explicitly false")
}

// TestPythonSkipCodegenAtRuntimeCallSucceeds verifies that an
// opted-in Python module (the new default) can run `dagger call`
// end-to-end: the runtime uses the committed sdk/** and
// _dagger_main.py without re-running codegen.
func (ModuleSuite) TestPythonSkipCodegenAtRuntimeCallSucceeds(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type

@object_type
class Test:
    @function
    def hello(self) -> str:
        return "ok"
`
	modGen := modInit(t, c, "python", source)

	out, err := modGen.
		With(daggerQuery(`{hello}`)).
		Stdout(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"hello":"ok"}`, out)
}

// TestPythonSkipCodegenAtRuntimeMissingFilesFails verifies that when
// an opted-in module is missing a required generated file,
// requireGeneratedFiles surfaces an actionable error.
func (ModuleSuite) TestPythonSkipCodegenAtRuntimeMissingFilesFails(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type

@object_type
class Test:
    @function
    def hello(self) -> str:
        return "ok"
`
	modGen := modInit(t, c, "python", source).
		// Remove the generated runtime entrypoint — this is one of the
		// paths requireGeneratedFiles asserts exists.
		WithoutFile("src/test/_dagger_main.py")

	_, err := modGen.
		With(daggerQuery(`{hello}`)).
		Stdout(ctx)
	require.Error(t, err)
	require.True(t,
		strings.Contains(err.Error(), "dagger develop"),
		"error must mention `dagger develop`; got: %s", err.Error())
	require.True(t,
		strings.Contains(err.Error(), "_dagger_main.py"),
		"error must name the missing file; got: %s", err.Error())
}

// TestPythonSkipCodegenAtRuntimeRegenAfterSourceEdit verifies that
// editing the user source and running `dagger develop` regenerates
// _dagger_main.py with the updated dispatch table — the canonical
// sync step after source edits.
func (ModuleSuite) TestPythonSkipCodegenAtRuntimeRegenAfterSourceEdit(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type

@object_type
class Test:
    @function
    def hello(self) -> str:
        return "ok"
`
	modGen := modInit(t, c, "python", source)

	// Baseline: _dagger_main.py has a dispatch arm for hello but not
	// for something_new.
	before, err := modGen.
		File("src/test/_dagger_main.py").
		Contents(ctx)
	require.NoError(t, err)
	require.Contains(t, before, `case ("Test", "hello"):`)
	require.NotContains(t, before, `somethingNew`)

	// Edit the source: add a new function.
	updatedSource := source + `
    @function
    def something_new(self) -> str:
        return "new"
`
	modGen = modGen.WithNewFile("src/test/__init__.py", updatedSource)

	// Run dagger develop to regenerate.
	modGen = modGen.With(daggerExec("develop"))

	after, err := modGen.
		File("src/test/_dagger_main.py").
		Contents(ctx)
	require.NoError(t, err)
	require.Contains(t, after, `case ("Test", "somethingNew"):`,
		"regenerated entrypoint must dispatch the new function")
}
