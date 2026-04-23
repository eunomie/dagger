# Python SDK: skip codegen at runtime

## Goal

Make the Python SDK honor the `codegen.legacyCodegenAtRuntime=false`
opt-in, mirroring Go PR 2. When opted in:

- `PythonSdk.Codegen()` short-circuits: asserts the committed
  generated files exist and returns the context directory as-is, with
  no codegen exec.
- `PythonSdk.ModuleRuntime()` skips the four `WithSDK` codegen phases
  (analyze / merge / generate / entrypoint-gen from spec 2) entirely,
  going straight to `WithSource` + `WithInstall`.

Newly-initialized Python modules (`dagger init --sdk=python`) opt in
by default, matching Go's behavior at `cmd/dagger/module.go:369`.

This is spec 3 of 3. Spec 1 shipped the schematool-based codegen;
spec 2 shipped the static runtime entrypoint (`_dagger_main.py`).
Together they make committing the generated files both meaningful and
performant.

## Background

Today (post-spec 2), every Python module cold-start runs:

1. `load SDK: python` — pull Python SDK image
2. `module SDK: run codegen` — four phases (~4-9s depending on module size)
3. `uv sync` — install user deps
4. `dagger functions` / `dagger call` — invoke

Phase 2 is wasted work for a module whose source hasn't changed
between cold starts. The Go SDK already has an opt-in
(`codegen.legacyCodegenAtRuntime=false`) that commits generated files
to the repo and short-circuits the codegen pass. This spec ports that
opt-in to Python.

The Python SDK is loaded as a **module-SDK** (see
`core/sdk/loader.go:140`), not a first-class Go SDK. So the short-
circuit lives inside the Python SDK's Go-driver at
`sdk/python/runtime/main.go`, reading the flag via a new dagql getter
(mirroring spec 1's `experimentalFeatureEnabled` pattern).

## Proposal

### Architecture

```
engine core
  ├── load Python SDK (builtin module)
  ├── call PythonSdk.Codegen(modSrc, schema)
  └── call PythonSdk.ModuleRuntime(modSrc, schema)

sdk/python/runtime/main.go
  Load()
    m.LegacyCodegenAtRuntime = modSrc.codegenConfig().legacyCodegenAtRuntime
      (default true; explicit false = opted-in to skip)

  Codegen(...)
    if !m.LegacyCodegenAtRuntime:
      requireGeneratedFiles(ctx)           -- sdk/**, _dagger_main.py
      return GeneratedCode(ContextDir)     -- no codegen exec
    else:
      Common() -> WithSDK (phases 0-3) -> write gen.py + _dagger_main.py
      return GeneratedCode(modifiedSrcDir)

  ModuleRuntime(...)
    Common()                                -- conditionally skips WithSDK
      WithBase
      [WithSDK] (skipped when !LegacyCodegenAtRuntime)
      WithTemplate (if IsInit)
      WithSource
      WithUpdates
    WithInstall
    return Container (entrypoint=/runtime)
```

### Components

#### 1. `moduleSource.codegenConfig` dagql getter

New read-only field on `ModuleSource` in `core/schema/modulesource.go`,
registered alongside the existing setters/getters near line 194:

```go
dagql.Func("codegenConfig", s.moduleSourceCodegenConfig).
    Doc(`The codegen configuration for the module source
         (from the "codegen" section of dagger.json).`),
```

Handler, placed near the `experimentalFeatureEnabled` handler from
spec 1:

```go
func (s *moduleSourceSchema) moduleSourceCodegenConfig(
    _ context.Context,
    parentSrc *core.ModuleSource,
    args struct{},
) (*core.ModuleCodegenConfig, error) {
    if parentSrc.CodegenConfig == nil {
        return &core.ModuleCodegenConfig{}, nil
    }
    return parentSrc.CodegenConfig, nil
}
```

**`ModuleCodegenConfig` becomes a dagql-exposed type.** In
`core/modulesource.go`, add field tags + Type/TypeDescription:

```go
type ModuleCodegenConfig struct {
    AutomaticGitignore     *bool `field:"true" name:"automaticGitignore" doc:"Whether dagger-generated files are auto-appended to .gitignore. When false, the user commits generated files."`
    LegacyCodegenAtRuntime *bool `field:"true" name:"legacyCodegenAtRuntime" doc:"Whether the SDK re-runs codegen at runtime. When false, the SDK trusts committed generated files and skips codegen entirely."`
}

func (*ModuleCodegenConfig) Type() *ast.Type {
    return &ast.Type{NamedType: "ModuleCodegenConfig", NonNull: false}
}

func (*ModuleCodegenConfig) TypeDescription() string {
    return "Codegen configuration from a module source's dagger.json."
}
```

Plus the `dagql.Fields[*core.ModuleCodegenConfig]{}.Install(dag)`
registration in the `Install()` sequence of `modulesource.go`.

#### 2. Python SDK Go-driver changes (`sdk/python/runtime/main.go`)

**New struct field** on `PythonSdk`:

```go
// LegacyCodegenAtRuntime is true when the module has NOT opted into
// skip-codegen-at-runtime.  False means the user committed the
// generated files (sdk/** + src/<pkg>/_dagger_main.py) and we skip
// codegen during cold-starts.
// +private
LegacyCodegenAtRuntime bool
```

**Populate in `Load`** (after the existing `SelfCallsEnabled` block):

```go
cfg := modSource.CodegenConfig()
legacy, err := cfg.LegacyCodegenAtRuntime(ctx)
if err != nil {
    // Field absent in source dagger.json -> default legacy=true.
    m.LegacyCodegenAtRuntime = true
} else {
    m.LegacyCodegenAtRuntime = legacy
}
```

(Exact generated-client shape is discovered when `dagger develop` is
run against the Python SDK runtime in the implementation patch. The
pointer-typed `*bool` default-to-true behavior lives on the engine
side via `parentSrc.CodegenConfig`.)

**`Codegen` short-circuit:**

```go
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
        if err := m.requireGeneratedFiles(ctx); err != nil {
            return nil, err
        }
        // Return the context directory as-is, stripping the SDK-runtime
        // build artifact (same as the legacy path).  No codegen exec.
        return dag.GeneratedCode(
            m.ContextDir.WithoutDirectory("sdk/runtime"),
        ).
            WithVCSGeneratedPaths(m.genPaths()).
            WithVCSIgnoredPaths(m.ignorePaths()), nil
    }

    // Legacy-path logic (today's behavior) unchanged below.
    ignorePaths := []string{".venv", "**/__pycache__"}
    genPaths := []string{}
    if m.VendorPath != "" {
        ignorePaths = append(ignorePaths, m.VendorPath)
        genPaths = []string{m.VendorPath + "/**"}
    }
    return dag.
        GeneratedCode(
            m.Container.Directory(m.ContextDirPath).
                WithoutDirectory("sdk/runtime"),
        ).
        WithVCSGeneratedPaths(genPaths).
        WithVCSIgnoredPaths(ignorePaths), nil
}
```

(The `genPaths()` / `ignorePaths()` helpers are small wrappers that
return the same lists the legacy path builds — factored out for
reuse.)

**`ModuleRuntime` short-circuit:** Modify `Common()` to conditionally
skip `WithSDK(introspectionJSON)` when `!m.LegacyCodegenAtRuntime`.
All other `Common()` steps run unchanged:

```go
func (m *PythonSdk) Common(
    ctx context.Context,
    modSource *dagger.ModuleSource,
    introspectionJSON *dagger.File,
) (*PythonSdk, error) {
    _, err := m.Load(ctx, modSource)
    if err != nil {
        return nil, err
    }
    _, err = m.WithBase()
    if err != nil {
        return nil, err
    }

    builder := m
    if m.LegacyCodegenAtRuntime {
        builder = builder.WithSDK(introspectionJSON)
    }
    // WithTemplate is a no-op when not in init mode; safe to keep.
    builder = builder.WithTemplate().WithSource().WithUpdates()
    return builder, nil
}
```

`ModuleRuntime` is unchanged; it calls `Common()` and then
`WithInstall()`, which now runs against a container that skipped
`WithSDK` when opted-in.

#### 3. `requireGeneratedFiles` helper

New method on `PythonSdk` at `sdk/python/runtime/main.go`:

```go
// requireGeneratedFiles ensures the module's committed generated
// files are present when legacyCodegenAtRuntime is off.  Returns an
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

Note: the check treats `sdk/` as a directory (presence check) rather
than walking the tree. The assumption is that if the user has
`sdk/` committed, it was produced by `dagger develop` and is
internally consistent. Explicit checks for individual files inside
`sdk/` (e.g. `sdk/src/dagger/client/gen.py`) can be added later if
needed.

#### 4. `dagger init --sdk=python` writes the flags

In `cmd/dagger/module.go` around line 369, generalize the existing
Go-specific helper. The current code:

```go
if sdk == "go" {
    configPath := filepath.Join(contextDirPath, srcRootSubPath, modules.Filename)
    if err := setGoSDKSkipRuntimeCodegen(configPath); err != nil {
        return fmt.Errorf("enable skip-codegen-at-runtime: %w", err)
    }
}
```

Becomes:

```go
switch sdk {
case "go", "python":
    configPath := filepath.Join(contextDirPath, srcRootSubPath, modules.Filename)
    if err := setSDKSkipRuntimeCodegen(configPath); err != nil {
        return fmt.Errorf("enable skip-codegen-at-runtime: %w", err)
    }
}
```

Rename `setGoSDKSkipRuntimeCodegen` → `setSDKSkipRuntimeCodegen`. The
helper body doesn't read the SDK kind — it only flips two JSON fields
in `dagger.json`. Callers elsewhere (e.g. `dagger develop`-time) are
unchanged by the rename.

### Data flow and invariants

See "Architecture" above. Invariants the implementation must preserve:

1. **When opted in and required files exist, the runtime never runs
   codegen.** The four phases (analyze / merge / generate /
   entrypoint-gen) are skipped entirely. `dagger call` and `dagger
   functions` go from `uv sync` straight to runtime dispatch.

2. **`dagger develop` always runs codegen.** The develop command
   invokes `Codegen` independently of `ModuleRuntime`; it regenerates
   `gen.py` + `_dagger_main.py` from the current user source,
   regardless of `legacyCodegenAtRuntime`. This is the canonical way
   to keep the committed files in sync.

3. **New init defaults to opted-in.** `dagger init --sdk=python`
   produces a module with `codegen.legacyCodegenAtRuntime=false` +
   `codegen.automaticGitignore=false`. Users see the committed
   generated files in their worktree from day one.

4. **Legacy modules unchanged.** A Python module with no `codegen`
   section or with `legacyCodegenAtRuntime=true` runs the full codegen
   pipeline at every cold-start — today's behavior. No migration
   required.

5. **`dagger.json` semantics unchanged.** The `ModuleCodegenConfig`
   struct, its JSON shape, and engine-core parsing already exist from
   Go PR 2. Spec 3 only adds a dagql getter for existing in-memory
   config.

6. **`sdk/python/runtime` self-opt-in stays intact.** The Python SDK
   runtime itself was opted into Go's skip-codegen-at-runtime mode in
   an earlier patch (`python-sdk-runtime-skip-codegen`). Spec 3
   doesn't touch it; the Go SDK's skip-codegen behavior continues to
   govern the Python SDK runtime's own cold-start.

### Error handling

- Required file missing → `"module %q has codegen.legacyCodegenAtRuntime=false but required generated path %q is missing. Run \`dagger develop\` to regenerate."` (same UX as Go).
- `modSource.codegenConfig()` GraphQL call fails → wrapped with `"runtime module load: read codegen config: %w"`.
- Absent `*bool` field → `m.LegacyCodegenAtRuntime = true` (legacy path; no surprising opt-in).
- Stale committed `_dagger_main.py` / `gen.py` (user edited source without `dagger develop`) → not detected; runtime uses the committed files. Same contract as Go's committed-codegen story.

## Non-goals

- **Automatic staleness detection** (compare source mtime to committed file timestamp).
- **Trimming the vendored `sdk/` tree** to shrink committed size — follow-up cleanup.
- **Non-vendored Python module layout** — spec 3.1 if/when promoted.
- **Extending the opt-in to TypeScript / Java / Elixir / PHP** — out of scope here; each SDK would follow this same pattern separately.
- **Modifying `sdk/python/runtime/` itself** — it's already opted in via the Go SDK's mechanism (earlier patch).

## Testing

### Integration — Go

New `core/integration/module_python_skip_codegen_test.go`:

- `TestPythonSkipCodegenAtRuntimeDefault` — `dagger init --sdk=python` writes the two flags; read `dagger.json` and assert both are `false`.
- `TestPythonSkipCodegenAtRuntimeCallSucceeds` — init a Python module, run `dagger call container-echo --string-arg=hi stdout`, assert `"hi"`.
- `TestPythonSkipCodegenAtRuntimeMissingFilesFails` — init, delete `src/test/_dagger_main.py`, run `dagger call`, assert the error mentions `"run \`dagger develop\`"`.
- `TestPythonSkipCodegenAtRuntimeRegenAfterSourceEdit` — init, modify the user source to add a new function, run `dagger develop`, verify the regenerated `_dagger_main.py` contains the new dispatch arm.

Plus: existing `TestSelfCalls/python`, `TestSelfCallsOffPython`, and `TestPythonStaticEntrypoint*` must continue to pass. They use `modInit`, which after spec 3's `dagger init` default will produce opted-in modules — they implicitly exercise the skip-codegen path. No regression allowed.

### Unit

Minimal unit-test surface:
- The `setSDKSkipRuntimeCodegen` rename is a straight rename; existing Go unit tests for it (if any) just get their reference updated.
- No new Python unit tests needed — all the interesting behavior is integration-level.

## Rollout

Five stg patches on top of spec 2 (stack grows 32 → 37):

1. `core/schema: codegenConfig getter on ModuleSource`  
   New dagql field + `field:"true"` tags on `ModuleCodegenConfig` + `Install` registration. Additive.

2. `sdk(python/runtime): honor legacyCodegenAtRuntime in Codegen`  
   Add `LegacyCodegenAtRuntime` field, populate in `Load`, branch in `Codegen`, add `requireGeneratedFiles`. Regenerate `sdk/python/runtime/dagger.gen.go`.

3. `sdk(python/runtime): skip WithSDK in ModuleRuntime when opted in`  
   Adjust `Common()` to conditionally skip `WithSDK(introspectionJSON)`. All other steps unchanged.

4. `cmd/dagger: dagger init --sdk=python writes legacyCodegenAtRuntime=false`  
   Rename `setGoSDKSkipRuntimeCodegen` → `setSDKSkipRuntimeCodegen`; extend the switch in `cmd/dagger/module.go` to cover `go` + `python`.

5. `core/integration: python skip-codegen-at-runtime tests`  
   Four new tests covering the opted-in path + regression-check for spec 1/2 tests (implicitly via modInit default).

Each patch:
- Compiles (`go build ./...`, `pytest` green)
- Tests pass before the next patch
- Committed via `stg new` with `Signed-off-by: Yves Brissaud <yves@dagger.io>`, no `Co-Authored-By`

## No further specs planned

Spec 3 completes the three-spec arc opened at the start of this
branch:

- **Spec 1** — self-calls via schematool (✅ shipped)
- **Spec 2** — static runtime entrypoint (✅ shipped)
- **Spec 3** — skip codegen at runtime (this spec)

After spec 3 the Python SDK has feature parity with the Go SDK for
the skip-codegen-at-runtime story. Optional follow-ups (not spec'd
here):

- Trim committed `sdk/**` tree.
- Extend the pattern to other SDKs (TypeScript / Java / Elixir / PHP).
- Non-vendored Python layout if it's promoted.
