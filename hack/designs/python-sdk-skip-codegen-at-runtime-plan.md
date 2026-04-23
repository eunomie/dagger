# Python SDK skip-codegen-at-runtime (spec 3) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Python SDK honor the `codegen.legacyCodegenAtRuntime=false` opt-in (mirroring Go PR 2): when set, `Codegen` short-circuits after asserting committed generated files exist, and `ModuleRuntime` skips the four `WithSDK` codegen phases entirely. `dagger init --sdk=python` defaults new modules to opted-in.

**Architecture:** A new dagql getter `moduleSource.codegenConfig` exposes the existing `ModuleCodegenConfig` struct so the Python SDK's Go-driver (`sdk/python/runtime/main.go`) can branch on `legacyCodegenAtRuntime`. Branching lives in two places: `Codegen` adds a short-circuit that asserts generated paths exist and returns the context directory as-is; `Common()` (shared by `Codegen`/`ModuleRuntime`/`ModuleTypesExp`) conditionally skips `WithSDK`. `cmd/dagger/module.go` generalizes the existing `setGoSDKSkipRuntimeCodegen` helper to apply for `--sdk=python` too.

**Tech Stack:** Go (engine schema + Python SDK Go-driver), Python (regenerated runtime client bindings via `dagger develop`), Go integration tests.

**Spec reference:** `hack/designs/python-sdk-skip-codegen-at-runtime.md`

**Stack context:** 33 patches on the `no-codegen-at-runtime` branch (the tip is `python-sdk-skip-codegen-at-runtime-design`). Spec 3's implementation stacks 5 patches on top → 38. Use `stg new` / `stg refresh`; every commit ends with `Signed-off-by: Yves Brissaud <yves@dagger.io>`; no `Co-Authored-By`.

---

## File structure (before tasks)

**Modified files:**
- `core/modules/config.go` — add `field:"true"` tags, `Type()`, `TypeDescription()` methods on `ModuleCodegenConfig`.
- `core/schema/modulesource.go` — register `dagql.Fields[*modules.ModuleCodegenConfig]{}.Install(dag)`; add `dagql.Func("codegenConfig", ...)` field on ModuleSource; add handler `moduleSourceCodegenConfig`.
- `sdk/python/runtime/main.go` — add `LegacyCodegenAtRuntime` field on `PythonSdk`; populate in `Load`; branch in `Codegen`; add `requireGeneratedFiles` helper; branch `WithSDK` call in `Common()`.
- `sdk/python/runtime/dagger.gen.go` + `sdk/python/runtime/internal/dagger/dagger.gen.go` — regenerated after the engine schema change in Task 1 adds the `codegenConfig` field.
- `cmd/dagger/module.go` — rename `setGoSDKSkipRuntimeCodegen` → `setSDKSkipRuntimeCodegen`; extend the init-time switch to `case "go", "python":`.

**New files:**
- `core/integration/module_python_skip_codegen_test.go` — four integration tests (default-flag check, call succeeds, missing-file error, regen-after-source-edit).

---

## Task 1: `codegenConfig` dagql getter on `ModuleSource`

**Files:**
- Modify: `core/modules/config.go:283-298` (add field tags + Type/TypeDescription)
- Modify: `core/schema/modulesource.go` (field registration around line 194-204, handler above line 2065, Install registration around line 286)

### Step 1: Add field tags + dagql methods to `ModuleCodegenConfig`

- [ ] **Edit `core/modules/config.go`**

Locate:

```go
type ModuleCodegenConfig struct {
	// Whether to automatically generate a .gitignore file for this module.
	AutomaticGitignore *bool `json:"automaticGitignore,omitempty"`
```

Replace the entire `ModuleCodegenConfig` struct declaration (currently lines 283-298) with:

```go
type ModuleCodegenConfig struct {
	// Whether to automatically generate a .gitignore file for this module.
	AutomaticGitignore *bool `field:"true" name:"automaticGitignore" doc:"Whether dagger-generated files are auto-appended to .gitignore. When false, the user commits generated files." json:"automaticGitignore,omitempty"`

	// LegacyCodegenAtRuntime controls whether the SDK runs codegen
	// during runtime operations (dagger call, dagger functions, etc.).
	// When explicitly false, the SDK trusts the committed generated
	// files and skips the runtime codegen pass entirely. Codegen still
	// runs on dagger init and dagger develop.
	//
	// Currently honored by the Go and Python SDKs; other SDKs read but
	// ignore this field.
	//
	// Default (nil or true): run codegen at runtime (legacy behavior).
	LegacyCodegenAtRuntime *bool `field:"true" name:"legacyCodegenAtRuntime" doc:"Whether the SDK re-runs codegen at runtime. When false, the SDK trusts committed generated files and skips codegen entirely." json:"legacyCodegenAtRuntime,omitempty"`
}

func (*ModuleCodegenConfig) Type() *ast.Type {
	return &ast.Type{
		NamedType: "ModuleCodegenConfig",
		NonNull:   false,
	}
}

func (*ModuleCodegenConfig) TypeDescription() string {
	return "Codegen configuration from a module source's dagger.json."
}
```

(`ast` is already imported by this file via `github.com/vektah/gqlparser/v2/ast` — verify with `grep "vektah/gqlparser" core/modules/config.go`.)

### Step 2: Verify the struct still compiles

- [ ] **Build**

```bash
go build ./core/modules/...
```

Expected: exit 0.

If the `ast` import isn't present in `config.go`, add it:

```go
import (
	// existing imports
	"github.com/vektah/gqlparser/v2/ast"
)
```

### Step 3: Register `ModuleCodegenConfig` as a dagql type

- [ ] **Edit `core/schema/modulesource.go`**

Find the line:

```go
	dagql.Fields[*core.SDKConfig]{}.Install(dag)
	dagql.Fields[*modules.ModuleConfigClient]{}.Install(dag)
```

Add one line immediately after:

```go
	dagql.Fields[*modules.ModuleCodegenConfig]{}.Install(dag)
```

### Step 4: Add two fields on `ModuleSource`: `codegenConfig` + `legacyCodegenAtRuntime`

Two fields:
- `codegenConfig` returns the `ModuleCodegenConfig` struct (raw config — available for SDKs that need fine-grained access).
- `legacyCodegenAtRuntime` returns a `Boolean` with the default-true semantics (nil-pointer maps to true), so consumers don't have to implement that logic themselves.

- [ ] **Edit `core/schema/modulesource.go`**

Locate the existing `experimentalFeatureEnabled` registration (around line 206-210):

```go
		dagql.Func("experimentalFeatureEnabled", s.moduleSourceExperimentalFeatureEnabled).
			Doc(`Whether the given experimental feature is enabled on this module source.`).
			Args(
				dagql.Arg("feature").Doc(`The experimental feature to check.`),
			),
```

Immediately after that block, insert:

```go
		dagql.Func("codegenConfig", s.moduleSourceCodegenConfig).
			Doc(`The codegen configuration for the module source (from the "codegen" section of dagger.json).`),

		dagql.Func("legacyCodegenAtRuntime", s.moduleSourceLegacyCodegenAtRuntime).
			Doc(`Whether the SDK runs codegen at runtime (dagger call, dagger functions). ` +
				`Default true; the user opts into skip-codegen-at-runtime by setting ` +
				`codegen.legacyCodegenAtRuntime=false in dagger.json.`),
```

### Step 5: Add the two handler functions

- [ ] **Edit `core/schema/modulesource.go`**

Locate `moduleSourceExperimentalFeatureEnabled` (around line 2071):

```go
func (s *moduleSourceSchema) moduleSourceExperimentalFeatureEnabled(
	_ context.Context,
	parentSrc *core.ModuleSource,
	args struct {
		Feature core.ModuleSourceExperimentalFeature
	},
) (dagql.Boolean, error) {
	if parentSrc.SDK == nil {
		return false, nil
	}
	return dagql.Boolean(parentSrc.SDK.ExperimentalFeatureEnabled(args.Feature)), nil
}
```

Immediately after that function (keep the blank line), insert:

```go
func (s *moduleSourceSchema) moduleSourceCodegenConfig(
	_ context.Context,
	parentSrc *core.ModuleSource,
	_ struct{},
) (*modules.ModuleCodegenConfig, error) {
	if parentSrc.CodegenConfig == nil {
		// No codegen config set — return a zeroed config so callers
		// can read the (nil-pointer) bool fields without a nil deref.
		return &modules.ModuleCodegenConfig{}, nil
	}
	return parentSrc.CodegenConfig, nil
}

func (s *moduleSourceSchema) moduleSourceLegacyCodegenAtRuntime(
	_ context.Context,
	parentSrc *core.ModuleSource,
	_ struct{},
) (dagql.Boolean, error) {
	c := parentSrc.CodegenConfig
	if c == nil || c.LegacyCodegenAtRuntime == nil {
		// Default behavior: SDK runs codegen at runtime.
		return true, nil
	}
	return dagql.Boolean(*c.LegacyCodegenAtRuntime), nil
}

```

Verify `modules` is already imported at the top of the file:

```bash
grep -n "dagger/dagger/core/modules" core/schema/modulesource.go | head -3
```

If present, no import change needed. If somehow missing, add:

```go
import (
	// existing imports
	"github.com/dagger/dagger/core/modules"
)
```

### Step 6: Compile

- [ ] **Build**

```bash
go build ./core/schema/... ./core/modules/...
```

Expected: exit 0.

### Step 7: Smoke-test via schema introspection

- [ ] **Verify both new fields show up**

```bash
go run ./cmd/introspect introspect | jq '.__schema.types[] | select(.name=="ModuleSource") | .fields[] | select(.name=="codegenConfig" or .name=="legacyCodegenAtRuntime") | .name' | sort
```

Expected output:

```
"codegenConfig"
"legacyCodegenAtRuntime"
```

```bash
go run ./cmd/introspect introspect | jq '.__schema.types[] | select(.name=="ModuleCodegenConfig") | .fields[] | .name' | sort
```

Expected output (order may vary):

```
"automaticGitignore"
"legacyCodegenAtRuntime"
```

### Step 8: Run existing schema tests

- [ ] **Run**

```bash
go test ./core/schema/... -run ModuleSource -count=1 -timeout 60s
```

Expected: PASS.

### Step 9: Commit

- [ ] **Commit via stg**

```bash
stg new python-sdk-codegen-config-getter -m "$(cat <<'EOF'
core/schema: codegenConfig getter on ModuleSource

Expose the existing ModuleCodegenConfig struct as a dagql type so
SDK modules can read codegen configuration from the module source.
The Python SDK's Go-driver will use this in an upcoming patch to
branch on codegen.legacyCodegenAtRuntime.

Additive only: existing AutomaticGitignore / LegacyCodegenAtRuntime
fields gain `field:"true"` tags; ModuleCodegenConfig gains Type /
TypeDescription methods; new dagql.Func("codegenConfig", ...) on
ModuleSource; handler returns a zeroed config when unset.

Mirrors the spec-1 pattern for moduleSource.experimentalFeatureEnabled.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

Expected: the new patch is on top of the stack.

---

## Task 2: Honor `legacyCodegenAtRuntime` in `Codegen`

**Files:**
- Modify: `sdk/python/runtime/main.go` (add field, populate in `Load`, add helpers, branch in `Codegen`)
- Regenerate: `sdk/python/runtime/dagger.gen.go` + `sdk/python/runtime/internal/dagger/dagger.gen.go` + `sdk/python/runtime/dagger.json` (via `dagger develop`)

### Step 1: Regenerate the Python SDK runtime's client bindings

- [ ] **Run `dagger develop` against the Python SDK runtime**

```bash
hack/dev dagger -m sdk/python/runtime develop
```

This picks up the Task-1 schema change (new `codegenConfig` field, new `legacyCodegenAtRuntime` helper, new `ModuleCodegenConfig` dagql type) and regenerates `sdk/python/runtime/internal/dagger/dagger.gen.go`. Takes several minutes.

- [ ] **Verify the regenerated client exposes the new methods**

```bash
grep -n "func (r \*ModuleSource) LegacyCodegenAtRuntime\|func (r \*ModuleSource) CodegenConfig" sdk/python/runtime/internal/dagger/dagger.gen.go | head -4
```

Expected:
- `func (r *ModuleSource) CodegenConfig() *ModuleCodegenConfig` present
- `func (r *ModuleSource) LegacyCodegenAtRuntime(ctx context.Context) (bool, error)` present

If the dev engine serves cached output (as happened in spec 1), the regen may fail to pick up the new fields. In that case, manually add the stubs to `sdk/python/runtime/internal/dagger/dagger.gen.go` matching the pattern of `func (r *ModuleSource) ExperimentalFeatureEnabled(ctx, feature) (bool, error)`:

```go
// The codegen configuration for the module source (from the "codegen" section of dagger.json).
func (r *ModuleSource) CodegenConfig() *ModuleCodegenConfig {
	return &ModuleCodegenConfig{
		query: r.query.Select("codegenConfig"),
	}
}

// Whether the SDK runs codegen at runtime (dagger call, dagger functions). Default true; the user opts into skip-codegen-at-runtime by setting codegen.legacyCodegenAtRuntime=false in dagger.json.
func (r *ModuleSource) LegacyCodegenAtRuntime(ctx context.Context) (bool, error) {
	q := r.query.Select("legacyCodegenAtRuntime")
	var response bool
	q = q.Bind(&response)
	return response, q.Execute(ctx)
}

// Codegen configuration from a module source's dagger.json.
type ModuleCodegenConfig struct {
	query *querybuilder.Selection
}

// Whether dagger-generated files are auto-appended to .gitignore. When false, the user commits generated files.
func (r *ModuleCodegenConfig) AutomaticGitignore(ctx context.Context) (bool, error) {
	q := r.query.Select("automaticGitignore")
	var response bool
	q = q.Bind(&response)
	return response, q.Execute(ctx)
}

// Whether the SDK re-runs codegen at runtime. When false, the SDK trusts committed generated files and skips codegen entirely.
func (r *ModuleCodegenConfig) LegacyCodegenAtRuntime(ctx context.Context) (bool, error) {
	q := r.query.Select("legacyCodegenAtRuntime")
	var response bool
	q = q.Bind(&response)
	return response, q.Execute(ctx)
}
```

Place these near the existing `ModuleSource.ExperimentalFeatureEnabled` method and the `type ModuleSource` block.

### Step 2: Add `LegacyCodegenAtRuntime` field on `PythonSdk`

- [ ] **Edit `sdk/python/runtime/main.go`**

Locate the struct field list (around line 145-151):

```go
	// SelfCallsEnabled is true when the module source has the
	// SELF_CALLS experimental feature turned on (i.e. the user ran
	// `dagger init --with-self-calls` or `dagger develop --with-self-calls`).
	// When true, WithSDK runs the three-phase analyze -> merge -> generate
	// pipeline so gen.py contains bindings for the module's declared types.
	// +private
	SelfCallsEnabled bool
}
```

Insert the new field before the closing brace:

```go
	// LegacyCodegenAtRuntime is true when the module has NOT opted
	// into skip-codegen-at-runtime (i.e. the user has not set
	// codegen.legacyCodegenAtRuntime=false in dagger.json).
	// When false, Codegen short-circuits (trusting committed
	// generated files) and ModuleRuntime skips WithSDK's codegen
	// phases entirely.
	// +private
	LegacyCodegenAtRuntime bool
```

### Step 3: Populate `LegacyCodegenAtRuntime` in `Load`

- [ ] **Edit `sdk/python/runtime/main.go`**

Locate the `Load` function's self-calls block (around line 261-267):

```go
	selfCalls, err := modSource.ExperimentalFeatureEnabled(
		ctx, dagger.ModuleSourceExperimentalFeatureSelfCalls,
	)
	if err != nil {
		return nil, fmt.Errorf("runtime module load: check self-calls feature: %w", err)
	}
	m.SelfCallsEnabled = selfCalls
```

Immediately after that block (before `m.Discovery.Load(ctx, m)`), insert:

```go
	legacy, err := modSource.LegacyCodegenAtRuntime(ctx)
	if err != nil {
		return nil, fmt.Errorf("runtime module load: check legacy-codegen flag: %w", err)
	}
	m.LegacyCodegenAtRuntime = legacy
```

(The engine-side handler from Task 1 Step 5 returns `true` when the pointer is nil or the field is unset — see `moduleSourceLegacyCodegenAtRuntime`. That means the Python-client-side `modSource.LegacyCodegenAtRuntime(ctx)` returns `true` for legacy modules and `false` only when the user explicitly set `codegen.legacyCodegenAtRuntime=false`. No client-side default-handling needed.)

### Step 4: Add the `requireGeneratedFiles` helper

- [ ] **Edit `sdk/python/runtime/main.go`**

Place the helper alongside `WithSDK` and friends. Find a reasonable location (e.g. immediately after `WithSDK`'s closing brace — around line 440 post-spec-2) and insert:

```go
// requireGeneratedFiles ensures the module's committed generated
// files are present when legacyCodegenAtRuntime is off. Returns an
// actionable error if any required path is missing.
func (m *PythonSdk) requireGeneratedFiles(ctx context.Context) error {
	required := []string{
		// Full vendored SDK tree (covers sdk/src/dagger/client/gen.py
		// plus all other vendored files).
		path.Join(m.SubPath, m.VendorPath),
		// Generated runtime entrypoint (from spec 2).
		path.Join(m.SubPath, "src", m.PackageName, "_dagger_main.py"),
	}
	for _, rel := range required {
		exists, err := m.ContextDir.Exists(ctx, rel)
		if err != nil {
			return fmt.Errorf("check generated path %q: %w", rel, err)
		}
		if !exists {
			return fmt.Errorf(
				"module %q has codegen.legacyCodegenAtRuntime=false "+
					"but required generated path %q is missing. "+
					"Run `dagger develop` to regenerate.",
				m.ModName, rel)
		}
	}
	return nil
}
```

Verify `Directory.Exists(ctx, path)` is available on the regenerated client:

```bash
grep -n "func (r \*Directory) Exists" sdk/python/runtime/internal/dagger/dagger.gen.go | head -2
```

Expected: a match. If absent, the dev engine hasn't regenerated fully — re-run `hack/dev dagger -m sdk/python/runtime develop`.

### Step 5: Add `genPaths` / `ignorePaths` helper methods

- [ ] **Edit `sdk/python/runtime/main.go`**

Insert near `requireGeneratedFiles`:

```go
// genPaths returns the list of VCS-tracked (generated) paths for
// this module. Shared between the legacy Codegen path and the
// opted-in short-circuit.
func (m *PythonSdk) genPaths() []string {
	if m.VendorPath != "" {
		return []string{m.VendorPath + "/**"}
	}
	return []string{}
}

// ignorePaths returns the list of VCS-ignored paths for this module.
// Shared between the legacy Codegen path and the opted-in
// short-circuit.
func (m *PythonSdk) ignorePaths() []string {
	ignore := []string{".venv", "**/__pycache__"}
	if m.VendorPath != "" {
		ignore = append(ignore, m.VendorPath)
	}
	return ignore
}
```

### Step 6: Branch `Codegen` on `LegacyCodegenAtRuntime`

- [ ] **Edit `sdk/python/runtime/main.go`**

Replace the `Codegen` function body (currently at lines 155-182) with:

```go
// Generated code for the Python module
func (m *PythonSdk) Codegen(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.GeneratedCode, error) {
	m, err := m.Common(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}

	if !m.LegacyCodegenAtRuntime {
		// Opted-in path: the user committed sdk/** and _dagger_main.py.
		// Trust them; skip the codegen exec entirely.
		if err := m.requireGeneratedFiles(ctx); err != nil {
			return nil, err
		}
		return dag.
			GeneratedCode(m.ContextDir.WithoutDirectory("sdk/runtime")).
			WithVCSGeneratedPaths(m.genPaths()).
			WithVCSIgnoredPaths(m.ignorePaths()), nil
	}

	return dag.
		GeneratedCode(
			m.Container.Directory(m.ContextDirPath).
				WithoutDirectory("sdk/runtime")).
		WithVCSGeneratedPaths(m.genPaths()).
		WithVCSIgnoredPaths(m.ignorePaths()), nil
}
```

### Step 7: Build `sdk/python/runtime`

- [ ] **Compile**

```bash
cd sdk/python/runtime && go build ./... && cd -
```

Expected: exit 0.

If compile errors mention missing `CodegenConfig` / `LegacyCodegenAtRuntime` methods, the regenerated bindings from Step 3b didn't land — re-check `dagger.gen.go`.

### Step 8: Smoke-test the legacy (non-opted-in) path via playground

This verifies a module with **no** `codegen` section in `dagger.json` continues to run the full codegen pipeline (i.e. no regression).

- [ ] **Run via playground, in background**

```bash
PLAYGROUND_TIMEOUT=600 bash skills/engine-dev-testing/with-playground.sh '
set -e
mkdir -p /tmp/legacy && cd /tmp/legacy &&
dagger init --sdk=python --name=test --source=. &&
# Strip the codegen config so this module exercises the legacy path.
jq "del(.codegen)" dagger.json > tmp.json && mv tmp.json dagger.json &&
dagger functions
'
```

Poll with `TaskOutput`. Expected: `=== Playground: SUCCESS ===` and `container-echo` + `grep-dir` listed.

### Step 9: Smoke-test the opted-in path via playground

- [ ] **Run**

```bash
PLAYGROUND_TIMEOUT=600 bash skills/engine-dev-testing/with-playground.sh '
set -e
mkdir -p /tmp/optedin && cd /tmp/optedin &&
dagger init --sdk=python --name=test --source=. &&
# Flip the codegen config to opt-in (until Task 4 does this by default).
jq ".codegen = {automaticGitignore:false,legacyCodegenAtRuntime:false}" dagger.json > tmp.json && mv tmp.json dagger.json &&
dagger develop &&
dagger functions
'
```

Expected: SUCCESS. `dagger develop` writes the generated files to disk (committed-path invariant); `dagger functions` lists functions without re-running codegen.

### Step 10: Commit

- [ ] **Stage and commit via stg**

```bash
stg add sdk/python/runtime/main.go
stg add sdk/python/runtime/dagger.gen.go
stg add sdk/python/runtime/internal/dagger/dagger.gen.go
stg add sdk/python/runtime/dagger.json
stg new python-sdk-runtime-honor-legacy-flag -m "$(cat <<'EOF'
sdk(python/runtime): honor legacyCodegenAtRuntime in Codegen

When the module has opted into skip-codegen-at-runtime
(codegen.legacyCodegenAtRuntime=false in dagger.json), Codegen
short-circuits: it asserts the committed generated files exist
(sdk/** + src/<package>/_dagger_main.py) and returns the context
directory as-is, without running any of the four WithSDK phases
(analyze / merge / generate / entrypoint-gen) from spec 2.

Reads the flag via the new moduleSource.legacyCodegenAtRuntime dagql
field (default true; explicit false = opted in) added to the engine
schema in the prior patch. The regenerated runtime client bindings
are included in this patch.

Does NOT yet modify ModuleRuntime's WithSDK call — that ships in the
next patch.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

---

## Task 3: Skip `WithSDK` in `ModuleRuntime` via `Common()` branch

**Files:**
- Modify: `sdk/python/runtime/main.go::Common` (add conditional)

### Step 1: Branch `Common()` on `LegacyCodegenAtRuntime`

- [ ] **Edit `sdk/python/runtime/main.go`**

Locate `Common()` (around lines 221-249):

```go
func (m *PythonSdk) Common(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	// +optional
	introspectionJSON *dagger.File,
) (*PythonSdk, error) {
	// NB: In extension modules, Load is chainable.
	_, err := m.Load(ctx, modSource)
	if err != nil {
		return nil, err
	}
	_, err = m.WithBase()
	if err != nil {
		return nil, err
	}
	return m.
		WithSDK(introspectionJSON).
		WithTemplate().
		WithSource().
		WithUpdates(), nil
}
```

Replace the final `return` block with:

```go
	// When the module has opted into skip-codegen-at-runtime, skip the
	// WithSDK codegen phases entirely. The user's committed sdk/** and
	// src/<pkg>/_dagger_main.py are the sole source of truth at runtime;
	// Codegen's short-circuit has already verified their presence.
	builder := m
	if m.LegacyCodegenAtRuntime {
		builder = builder.WithSDK(introspectionJSON)
	}
	return builder.
		WithTemplate().
		WithSource().
		WithUpdates(), nil
```

### Step 2: Compile

- [ ] **Build**

```bash
cd sdk/python/runtime && go build ./... && cd -
```

Expected: exit 0.

### Step 3: Smoke-test the opted-in runtime path via playground

Verifies `dagger call` on an opted-in module actually invokes the module without re-running codegen at runtime.

- [ ] **Run**

```bash
PLAYGROUND_TIMEOUT=600 bash skills/engine-dev-testing/with-playground.sh '
set -e
mkdir -p /tmp/optedin-runtime && cd /tmp/optedin-runtime &&
dagger init --sdk=python --name=test --source=. &&
jq ".codegen = {automaticGitignore:false,legacyCodegenAtRuntime:false}" dagger.json > tmp.json && mv tmp.json dagger.json &&
dagger develop &&
dagger call container-echo --string-arg=hi stdout
'
```

Expected: `SUCCESS` and the output tail contains `hi`. The important signal: the `module SDK: run codegen` span should not appear after the first `dagger develop` — only `load SDK: python` + `load workspace` + execution.

### Step 4: Smoke-test the legacy path still works

- [ ] **Run**

```bash
PLAYGROUND_TIMEOUT=600 bash skills/engine-dev-testing/with-playground.sh '
set -e
mkdir -p /tmp/legacy-runtime && cd /tmp/legacy-runtime &&
dagger init --sdk=python --name=test --source=. &&
jq "del(.codegen)" dagger.json > tmp.json && mv tmp.json dagger.json &&
dagger call container-echo --string-arg=hi stdout
'
```

Expected: SUCCESS, `hi` in output. This path still runs the four codegen phases.

### Step 5: Commit

- [ ] **Commit via stg**

```bash
stg new python-sdk-runtime-skip-withsdk -m "$(cat <<'EOF'
sdk(python/runtime): skip WithSDK in ModuleRuntime when opted in

Common() now conditionally skips the WithSDK(introspectionJSON) call
(which runs the four codegen phases from spec 2: analyze / merge /
generate / entrypoint-gen) when the module has opted into
skip-codegen-at-runtime.

The rest of Common() — WithBase, WithTemplate (no-op when not in
init mode), WithSource, WithUpdates — runs unchanged. ModuleRuntime
then appends WithInstall as before.

Completes the runtime-side half of the skip-codegen-at-runtime story.
Together with the Codegen short-circuit from the prior patch, an
opted-in Python module has zero codegen exec at runtime: it goes from
load SDK -> load workspace -> uv sync -> runtime dispatch.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

---

## Task 4: `dagger init --sdk=python` writes `legacyCodegenAtRuntime=false`

**Files:**
- Modify: `cmd/dagger/module.go` (rename helper at line 1333, extend switch at line 369)

### Step 1: Rename `setGoSDKSkipRuntimeCodegen` to `setSDKSkipRuntimeCodegen`

- [ ] **Edit `cmd/dagger/module.go`**

Rename the function at line 1333 and update its doc comment. Locate:

```go
// setGoSDKSkipRuntimeCodegen patches the newly-generated dagger.json to
// opt this module out of runtime codegen. New Go modules created via
// `dagger init --sdk=go` default to the opt-in path: the generated
// files live in the repo (automaticGitignore=false) and `dagger call`
// skips `codegen generate-module` (legacyCodegenAtRuntime=false).
//
// Round-trips through modules.ModuleConfigWithUserFields so the output
// field order stays aligned with the engine's own exporter — this
// prevents cosmetic diffs between init-time and develop-time
// dagger.json serialization.
func setGoSDKSkipRuntimeCodegen(configPath string) error {
```

Replace with:

```go
// setSDKSkipRuntimeCodegen patches the newly-generated dagger.json to
// opt this module out of runtime codegen. Modules created via
// `dagger init --sdk=go` or `--sdk=python` default to the opt-in
// path: the generated files live in the repo
// (automaticGitignore=false) and runtime calls skip codegen
// (legacyCodegenAtRuntime=false).
//
// Round-trips through modules.ModuleConfigWithUserFields so the output
// field order stays aligned with the engine's own exporter — this
// prevents cosmetic diffs between init-time and develop-time
// dagger.json serialization.
func setSDKSkipRuntimeCodegen(configPath string) error {
```

### Step 2: Update the call site at line 369-374

- [ ] **Edit `cmd/dagger/module.go`**

Locate:

```go
			// For new Go modules, opt into skip-codegen-at-runtime by
			// default. This writes codegen.legacyCodegenAtRuntime=false
			// and codegen.automaticGitignore=false to the freshly-
			// exported dagger.json. Other SDKs don't support this mode
			// yet, so we only apply it for --sdk=go.
			if sdk == "go" {
				configPath := filepath.Join(contextDirPath, srcRootSubPath, modules.Filename)
				if err := setGoSDKSkipRuntimeCodegen(configPath); err != nil {
					return fmt.Errorf("enable skip-codegen-at-runtime: %w", err)
				}
			}
```

Replace with:

```go
			// For new Go and Python modules, opt into
			// skip-codegen-at-runtime by default. This writes
			// codegen.legacyCodegenAtRuntime=false and
			// codegen.automaticGitignore=false to the freshly-exported
			// dagger.json. Other SDKs don't support this mode yet.
			switch sdk {
			case "go", "python":
				configPath := filepath.Join(contextDirPath, srcRootSubPath, modules.Filename)
				if err := setSDKSkipRuntimeCodegen(configPath); err != nil {
					return fmt.Errorf("enable skip-codegen-at-runtime: %w", err)
				}
			}
```

### Step 3: Check for any remaining references to the old name

- [ ] **Verify no stragglers**

```bash
grep -rn "setGoSDKSkipRuntimeCodegen" .
```

Expected: zero matches.

If matches remain (e.g. in a test file), rename them to `setSDKSkipRuntimeCodegen`.

### Step 4: Build

- [ ] **Compile**

```bash
go build ./cmd/dagger/...
```

Expected: exit 0.

### Step 5: Run existing `cmd/dagger` tests

- [ ] **Run**

```bash
go test ./cmd/dagger/... -count=1 -timeout 120s
```

Expected: PASS.

### Step 6: Smoke-test `dagger init --sdk=python`

- [ ] **Verify new modules default to opted-in**

```bash
PLAYGROUND_TIMEOUT=300 bash skills/engine-dev-testing/with-playground.sh '
set -e
mkdir -p /tmp/init-defaults && cd /tmp/init-defaults &&
dagger init --sdk=python --name=test --source=. &&
cat dagger.json
'
```

Expected in the output: a `"codegen"` object with both `"automaticGitignore": false` and `"legacyCodegenAtRuntime": false`.

### Step 7: Commit

- [ ] **Commit via stg**

```bash
stg new python-sdk-init-default-skip-codegen -m "$(cat <<'EOF'
cmd/dagger: dagger init --sdk=python writes legacyCodegenAtRuntime=false

Newly-initialized Python modules opt into skip-codegen-at-runtime by
default, matching Go's behavior. The generated dagger.json gets
codegen.automaticGitignore=false and codegen.legacyCodegenAtRuntime=false,
the user commits gen.py + _dagger_main.py + sdk/** from day one, and
dagger call / dagger functions skip the four codegen phases at runtime.

The existing setGoSDKSkipRuntimeCodegen helper is renamed to
setSDKSkipRuntimeCodegen (its body is already SDK-agnostic). The
init-time gate becomes a switch: case "go", "python". Other SDKs
(typescript, java, elixir, php) continue the legacy path.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

---

## Task 5: Integration tests

**Files:**
- Create: `core/integration/module_python_skip_codegen_test.go`

### Step 1: Create the test file

- [ ] **Create `core/integration/module_python_skip_codegen_test.go`**

```go
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
```

### Step 2: Verify the file compiles

- [ ] **Build**

```bash
go build ./core/integration/...
```

Expected: exit 0.

### Step 3: Run each integration test

Each takes 5-15 min cold. Run in background, poll with `TaskOutput`.

- [ ] **Run `TestPythonSkipCodegenAtRuntimeDefault`**

```bash
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonSkipCodegenAtRuntimeDefault$' --pkg=./core/integration --test-verbose --timeout=20m
```

Expected: exit 0, `Void`.

- [ ] **Run `TestPythonSkipCodegenAtRuntimeCallSucceeds`**

```bash
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonSkipCodegenAtRuntimeCallSucceeds$' --pkg=./core/integration --test-verbose --timeout=20m
```

Expected: exit 0, `Void`.

- [ ] **Run `TestPythonSkipCodegenAtRuntimeMissingFilesFails`**

```bash
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonSkipCodegenAtRuntimeMissingFilesFails$' --pkg=./core/integration --test-verbose --timeout=20m
```

Expected: exit 0, `Void`.

- [ ] **Run `TestPythonSkipCodegenAtRuntimeRegenAfterSourceEdit`**

```bash
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonSkipCodegenAtRuntimeRegenAfterSourceEdit$' --pkg=./core/integration --test-verbose --timeout=20m
```

Expected: exit 0, `Void`.

### Step 4: Run spec 1 + spec 2 regression tests

The `dagger init` default change may incidentally flip existing tests through the opted-in path; confirm no regressions.

- [ ] **Run**

```bash
dagger call engine-dev test --run='^TestModuleSuite$/^TestSelfCalls' --pkg=./core/integration --test-verbose --timeout=30m
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonStaticEntrypoint' --pkg=./core/integration --test-verbose --timeout=30m
```

Expected: both invocations exit 0 with `Void`.

If anything fails, investigate and fix before moving on. Do not weaken any assertion.

### Step 5: Commit

- [ ] **Commit via stg**

```bash
stg add core/integration/module_python_skip_codegen_test.go
stg new python-sdk-skip-codegen-integration-tests -m "$(cat <<'EOF'
core/integration: python skip-codegen-at-runtime tests

Four integration tests covering the opted-in Python skip-codegen path:

  * TestPythonSkipCodegenAtRuntimeDefault — dagger init --sdk=python
    writes codegen.{legacyCodegenAtRuntime,automaticGitignore}=false
  * TestPythonSkipCodegenAtRuntimeCallSucceeds — dagger call on an
    opted-in module succeeds end-to-end without runtime codegen
  * TestPythonSkipCodegenAtRuntimeMissingFilesFails — deleting a
    required generated file surfaces an actionable error that
    mentions `dagger develop`
  * TestPythonSkipCodegenAtRuntimeRegenAfterSourceEdit — editing
    source + running `dagger develop` regenerates _dagger_main.py
    with the new dispatch arm

Spec 1 and spec 2 integration tests (TestSelfCalls*,
TestPythonStaticEntrypoint*) must continue passing — after this
patch the default dagger init produces opted-in modules, so they
implicitly exercise the skip-codegen path.

Completes spec 3, which is the final spec in the three-spec arc for
Python SDK static-at-runtime (spec 1: self-calls via schematool,
spec 2: static runtime entrypoint, spec 3: skip codegen at runtime).

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -5
```

---

## Final verification

### Step 1: Full integration sweep — python-related

- [ ] **Run**

```bash
dagger call engine-dev test --run='^TestModuleSuite$/^TestSelfCalls' --pkg=./core/integration --test-verbose --timeout=30m
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonStaticEntrypoint' --pkg=./core/integration --test-verbose --timeout=30m
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonSkipCodegenAtRuntime' --pkg=./core/integration --test-verbose --timeout=30m
```

Expected: all three invocations exit 0 with `Void`.

### Step 2: Go-SDK regression check (skip-codegen already shipped there)

- [ ] **Run**

```bash
dagger call engine-dev test --run='^TestModuleSuite$/^TestSelfCalls$/^go$' --pkg=./core/integration --test-verbose --timeout=20m
```

Expected: exit 0, `Void`.

### Step 3: Confirm the stack is clean

- [ ] **Inspect**

```bash
stg series --all
git status --porcelain
```

Expected: the top of the stack is `python-sdk-skip-codegen-integration-tests`, 5 new patches on top of `python-sdk-skip-codegen-at-runtime-design`, working tree clean (besides `.claude/scheduled_tasks.lock` which is a harness artifact).

---

## Out-of-scope reminders (do not implement in this plan)

- **Trimming the vendored `sdk/` tree** to reduce committed size. Can be addressed in a follow-up patch once the current stack is validated in production.
- **Non-vendored Python module layout** (where `gen.py` lives at `src/dagger_gen.py` rather than inside `sdk/`). All current modules use the vendored layout; revisit if a non-vendored path is promoted.
- **Extending the opt-in to TypeScript / Java / Elixir / PHP.** Each SDK would follow this same pattern separately.
- **Modifying `sdk/python/runtime/` itself.** It was already opted into Go's skip-codegen-at-runtime mode in an earlier patch on this branch (`python-sdk-runtime-skip-codegen`). Spec 3 does not touch it.
- **Automatic staleness detection** (comparing source mtime to committed file timestamp). The user contract is: "after editing source, run `dagger develop`", same as Go.
