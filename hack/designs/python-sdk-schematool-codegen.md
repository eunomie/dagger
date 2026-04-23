# Python SDK: schematool-based codegen pipeline

## Goal

Make `--with-self-calls` work for Python, using the same pattern the Go
SDK already uses. When self-calls is enabled, Python codegen becomes a
three-phase pipeline — **analyze** (AST) → **merge** (schematool) →
**generate** (Python generator) — so the emitted `gen.py` contains
bindings for the module's own declared types. When self-calls is off,
codegen runs plainly against the base schema (today's behavior).

Concrete success criterion: uncomment the Python case in
`core/integration/module_test.go::TestSelfCalls` (currently lines
6446-6464) and have it pass.

This is spec 1 of 2. Spec 2 will add `legacyCodegenAtRuntime` opt-in for
Python, mirroring Go PR 2.

## Background

Today, `sdk/python/runtime/main.go::WithSDK` runs the Python `codegen` CLI
against the engine's base+deps introspection JSON and writes the result
to `<module>/sdk/src/dagger/client/gen.py` (vendored) or
`<module>/src/dagger_gen.py` (non-vendored). The module's own declared
types are **not** represented in the generated bindings: a module that
self-references its own types through the generated `dag.*` client
(e.g. `dag.MyModule().Foo().Bar()` from within the module) gets
bindings that don't know about `Foo`. This is why the Python case in
`TestSelfCalls` is currently commented out.

The user opts in per-module via the existing CLI:

```
dagger init --sdk=python --with-self-calls
# or
dagger develop --with-self-calls
```

Both set the `SELF_CALLS` experimental feature on the module source
(`SDKConfig.Experimental["SELF_CALLS"] = true`). Engine-side,
`core/schema/modulesource.go::isSelfCallsEnabled(src)` checks this.

The Go SDK solved this in PR 1 by:

1. AST-scanning the module's Go source (`cmd/codegen/astscan`) to produce
   a language-agnostic `schematool.ModuleTypes` JSON.
2. Merging that JSON into the engine's introspection schema via
   `schematool.Merge`.
3. Generating Go bindings against the merged schema.

For the Python SDK we already have the ingredients:

- `sdk/python/src/dagger/mod/_analyzer/` (landed via #11803) is a pure-AST
  type analyzer that produces a `ModuleMetadata` Python dataclass from
  user source without importing the module.
- `cmd/codegen/schematool` (landed in PR 1) is the language-agnostic
  merger. `ModuleTypes` is explicitly documented as "the
  language-agnostic input shape that a SDK's Phase-1 source analyzer
  produces for Merge".
- `cmd/codegen merge-schema` (landed in PR 1) is a standalone Go CLI
  that eats `introspectionJSON + moduleTypesJSON → extendedSchemaJSON`.

What's missing:

1. make `_analyzer` emit `schematool.ModuleTypes` JSON,
2. ship `merge-schema` into the Python SDK rootfs,
3. rewire `WithSDK` to orchestrate analyze → merge → generate **when
   self-calls is on**,
4. give the Python SDK runtime (a Dagger module) a way to read the
   self-calls state from `modSource` — there's currently no dagql
   getter for it (only setters: `withExperimentalFeatures` /
   `withoutExperimentalFeatures`). We add one.

Note: Python SDK already does **not** implement `AsModuleTypes` (its
function is named `ModuleTypesExp`, so `sdk.funcs["moduleTypes"]`
misses it). This matches Go's pattern where the SDK's codegen path
handles self-types internally instead of relying on engine-side
pre-enrichment. No change needed there.

## Proposal

### Architecture

When `self-calls` is ON (module source has `SELF_CALLS` experimental
feature):

```
engine.introspectionJSON (base + deps)
              │
              ▼
  python -m dagger.mod._analyzer emit \
      --module-source-dir src/<pkg> \
      --main-object <Name> --module-name <name> \
      --introspection-json /schema.json \
      --output /module-types.json
              │
              ▼
  merge-schema \
      --introspection-json-path /schema.json \
      --module-types-path      /module-types.json \
      --output-path            /extended-schema.json
              │
              ▼
  codegen generate -i /extended-schema.json -o /gen.py
```

When self-calls is OFF: **plain codegen only**, identical to today's
behavior:

```
engine.introspectionJSON (base + deps)
              │
              ▼
  codegen generate -i /schema.json -o /gen.py
```

All phases run in the same codegen container orchestrated by
`sdk/python/runtime/main.go::WithSDK`. Each exec is a separate layer so
buildkit caches them independently.

### Components

#### 1. `dagger.mod._analyzer` CLI

New CLI entry point inside the existing analyzer package. Lives in
`sdk/python/src/dagger/mod/_analyzer/__main__.py`.

Invocation: `python -m dagger.mod._analyzer`. Single subcommand for
now:

- `emit` — run AST analysis, serialize to `schematool.ModuleTypes`
  JSON, write to stdout or `--output` file.

Flags:

- `--module-source-dir PATH` — root of the user's Python package; the
  CLI walks it using existing `_discovery.find_source_files()`
  semantics (recurse into subpackages, skip `_`-prefixed dirs,
  `__init__.py` last).
- `--main-object NAME` — module's main object class name. If unset,
  reads `DAGGER_MAIN_OBJECT` env var (already set by the runtime
  container in `WithSource`).
- `--module-name NAME` — module name. If unset, reads `DAGGER_MODULE`.
- `--introspection-json PATH` — base schema used to distinguish
  user-declared types from references to upstream types (e.g. a
  function returning `dagger.Container` must not re-declare
  `Container`).
- `--output PATH` — output path (default stdout).

Implementation is a thin wrapper around the existing `analyze_module()`
+ a new `_to_schematool()` mapper (see 2.2).

Errors surface as non-zero exit with stderr diagnostics. Existing
`AnalysisError` / `ValidationError` carry source locations.

#### 2. `to_schematool_json()` serializer

New module: `sdk/python/src/dagger/mod/_analyzer/schematool.py`. Pure
mapping function, no I/O:

```python
def to_schematool_json(metadata: ModuleMetadata,
                      base_schema: dict) -> dict:
    """Convert ModuleMetadata into schematool.ModuleTypes JSON.

    Types already present in base_schema are filtered out (they're
    references, not declarations).
    """
```

Mapping table:

| ModuleMetadata                        | ModuleTypes                  |
|---------------------------------------|------------------------------|
| `ModuleMetadata.name`                 | `name`                       |
| `ModuleMetadata.description`          | `description`                |
| `ObjectTypeMetadata`                  | `ObjectDef`                  |
| `ObjectTypeMetadata.fields[]`         | `fields[]`                   |
| `ObjectTypeMetadata.functions[]`      | `functions[]`                |
| `ObjectTypeMetadata.constructor`      | `constructor`                |
| `EnumTypeMetadata`                    | `EnumDef`                    |
| `EnumMemberMetadata`                  | `EnumValue`                  |
| `FunctionMetadata`                    | `Function`                   |
| `ParameterMetadata`                   | `FuncArg`                    |
| `FieldMetadata`                       | `FieldDef`                   |
| `ResolvedType`                        | `TypeRef`                    |

`ResolvedType → TypeRef` normalizes kinds to
`OBJECT` / `INTERFACE` / `ENUM` / `SCALAR` / `LIST` / `NON_NULL`
(wrapped nested structures).

Interfaces: the Python SDK doesn't expose user-declared interfaces
today, so `interfaces[]` is always empty.

#### 3. Python runtime driver (`sdk/python/runtime/main.go::WithSDK`)

**Self-calls gating:**

`PythonSdk.Load` (or `Common`, whichever is the earliest point where
`ModSource` is available) queries `modSource.experimentalFeatureEnabled("SELF_CALLS")`
and caches the boolean in a new field `m.SelfCallsEnabled`. The query
uses the new dagql getter added in §5.

`WithSDK` branches on `m.SelfCallsEnabled`:

- **Off** (today's behavior): single-exec `codegen generate -i /schema.json -o /gen.py`.
- **On**: the three-phase composition below.

Three-phase block:

```go
if introspectionJSON != nil {
    userSourceDir := filepath.Join(m.ContextDirPath, m.SubPath,
                                   "src", m.PackageName)

    ctr := m.Container.
        WithMountedFile(SchemaPath, introspectionJSON).
        WithMountedDirectory(m.ContextDirPath, m.ContextDir).
        WithMountedDirectory("/sdk", m.SdkSourceDir).
        WithWorkdir("/sdk").
        WithMountedFile("/usr/local/bin/merge-schema",
                        m.SdkSourceDir.File("dist/merge-schema"))

    // Phase 1: analyze (uv run; analyzer is part of the dagger pkg)
    ctr = ctr.WithExec([]string{
        "uv", "run", "--isolated", "--frozen", "--package", "dagger",
        "python", "-m", "dagger.mod._analyzer", "emit",
        "--module-source-dir",  userSourceDir,
        "--main-object",        m.MainObjectName,
        "--module-name",        m.ModName,
        "--introspection-json", SchemaPath,
        "--output",             "/module-types.json",
    })

    // Phase 2: merge (bundled Go binary)
    ctr = ctr.WithExec([]string{
        "merge-schema",
        "--introspection-json-path", SchemaPath,
        "--module-types-path",       "/module-types.json",
        "--output-path",             "/extended-schema.json",
    })

    // Phase 3: generate (reuse existing shiv-or-uv-run logic for codegen)
    var codegenCmd []string
    if m.Discovery.SdkHasFile("dist/codegen") {
        ctr = ctr.
            WithMountedCache("/root/.shiv", dag.CacheVolume("shiv")).
            WithMountedFile("/usr/local/bin/codegen",
                            m.SdkSourceDir.File("dist/codegen"))
        codegenCmd = []string{"codegen"}
    } else {
        codegenCmd = []string{
            "uv", "run", "--isolated", "--frozen", "--package", "codegen",
            "python", "-m", "codegen",
        }
    }
    genFile := ctr.WithExec(append(codegenCmd,
        "generate", "-i", "/extended-schema.json", "-o", "/gen.py",
    )).File("/gen.py")

    // … existing AddFile(genPath, genFile) …
}
```

The three-phase block mounts `m.ContextDir` at `m.ContextDirPath`
before running the analyzer — this is the same mount that `WithSource`
performs later in the composition, promoted into `WithSDK` so phase 1
can read the user's `.py` files. `userSourceDir` points at the user's
Python package root (matching the layout the template and
`_discovery.find_source_files()` already assume).

For greenfield `dagger init --sdk=python` the user package doesn't
exist yet (the template is written later by `WithTemplate`). In that
case `find_source_files()` returns an empty list, the analyzer emits a
`ModuleTypes` with zero objects, merge is a no-op, and codegen runs
against the unmodified base schema. Identical to today's first-time
init behavior.

The existing `dist/codegen` fallback ("run via `uv run` when the shiv
isn't present") remains intact for phase 3. The analyzer (phase 1) is
always invoked via `uv run --package dagger` since there's no shiv for
it today; a `dist/analyzer` shiv can come later as a perf tweak.

The existing prebuilt `.dagger-build/gen.py` short-circuit
(introduced earlier for engine-dev rootfs performance) **is removed**:
its output is only correct when `ModuleTypes` is empty, which we can't
assume without running the analyzer. Bringing the optimization back
conditionally is a follow-up.

#### 4. Engine image bundling

`toolchains/engine-dev/build/sdk.go::pythonSDKContent` adds:

```go
WithFile("dist/merge-schema", build.binary("./cmd/codegen", false, false))
```

Reusing `cmd/codegen` as `merge-schema` (the binary has a `merge-schema`
subcommand) keeps us to one Go binary. The binary is only consumed on
the self-calls path, but it's small enough that unconditionally
shipping it is simpler than gating the bundle.

The existing `dist/codegen` and `pythonGenPy` logic stay as-is. The
prebuilt `.dagger-build/gen.py` short-circuit continues to work for
non-self-calls modules (which is the vast majority). For self-calls
modules it is bypassed — the prebuilt gen.py only reflects the base
schema, not the module's self types.

#### 5. Dagql getter for experimental features

Add a new field on `ModuleSource`:

```go
dagql.Func("experimentalFeatureEnabled",
           s.moduleSourceExperimentalFeatureEnabled).
    Args(dagql.Arg("feature", dagql.String)).
    Doc("Whether the given experimental feature is enabled on " +
        "this module source.")
```

Handler:

```go
func (s *moduleSourceSchema) moduleSourceExperimentalFeatureEnabled(
    ctx context.Context,
    src dagql.ObjectResult[*core.ModuleSource],
    args struct{ Feature string },
) (dagql.Boolean, error) {
    return dagql.Boolean(
        src.Self().SDK.ExperimentalFeatureEnabled(
            core.ModuleSourceExperimentalFeature(args.Feature))), nil
}
```

This is additive; no existing callers or tests break. Other SDK
modules (TypeScript, Java, etc.) can use it to port the same pattern
later.

### Data flow

```
┌─ engine core ──────────────────────────────────────────┐
│ build base+deps introspection JSON                     │
│ load Python SDK (builtin module)                       │
│ call pythonSDK.Codegen(source, introspectionJSON)      │
└──────────────┬─────────────────────────────────────────┘
               │
               ▼
┌─ sdk/python/runtime ───────────────────────────────────┐
│ Discovery.Load → paths, main object                    │
│ WithBase    → python base image                        │
│ WithSDK     → three-phase codegen block (§3)           │
│   exec 1: python -m dagger.mod._analyzer emit          │
│   exec 2: merge-schema                                 │
│   exec 3: codegen generate                             │
│ AddFile(sdk/src/dagger/client/gen.py, genFile)         │
│ WithTemplate / WithSource / WithInstall                │
└──────────────┬─────────────────────────────────────────┘
               │
               ▼
  ModuleRuntime container handed back to engine
```

### Invariants

- **Self-calls default off = no behavior change**: modules without
  `--with-self-calls` run through exactly today's plain-codegen path,
  down to the prebuilt `.dagger-build/gen.py` short-circuit. No
  codegen latency regression for the common case.
- **Deterministic output**: for identical module source + engine schema
  + self-calls state, `gen.py` is byte-identical across runs. Required
  later in spec 2 so users can commit the file without worrying about
  generator-caused churn.
- **No duplicated types**: `to_schematool_json()` filters out types
  that exist in the base schema (by name). `schematool.Merge` returns
  an error on name collision, which catches analyzer-side bugs rather
  than silently overwriting upstream types.
- **No user-code import**: `_analyzer` is pure AST. Phase 1 runs
  before any `pip`/`uv sync` of user deps, so missing user deps do
  not break codegen.
- **Vendor layout unaffected**: the `VendorPath != ""` path continues to
  write `gen.py` to `sdk/src/dagger/client/gen.py`; the non-vendored
  path continues to write to `src/dagger_gen.py`. Only the source file
  content changes, and only when self-calls is on.
- **`ModuleTypesExp` unchanged**: the `--register` full-runtime typedefs
  path is not touched. Replacing it with the AST analyzer is a
  separate optimization (not in this spec).

### Error handling

- **Analyzer failure** (bad Python syntax, unresolved type, validation
  error): non-zero exit, stderr diagnostic with file + line. No silent
  fallback — failing loudly is better than generating bindings that
  silently omit user types.
- **Merge collision** (user named a class after an upstream type, e.g.
  `Container`): `merge-schema` returns a non-zero exit with the
  conflict name. Fix: user renames the offending type.
- **Missing introspection JSON**: same behavior as today —
  codegen/analyze skip their schema-lookup validation and proceed;
  merge-schema will catch any resulting collisions.

## Non-goals

- **`legacyCodegenAtRuntime` opt-in for Python** — spec 2.
- **`dagger init --sdk=python` writing codegen config** — spec 2.
- **Committing `gen.py` and skipping codegen at runtime** — spec 2.
- **Prebuilt `.dagger-build/gen.py` for self-calls modules** — the
  prebuilt gen.py only reflects the engine's base schema, so
  self-calls modules bypass it. Making the prebuilt path honor
  self-calls would require knowing the module's types at engine-build
  time, which is not possible. Out of scope.
- **Shiv-bundled `dist/analyzer`** — possible follow-up for prod
  parity with `dist/codegen`. Not required for correctness.
- **Replacing `ModuleTypesExp --register` with AST analyzer** —
  separate optimization, independent of this spec.
- **Changes to other SDKs** — TypeScript, Java, Elixir, etc. — out of
  scope.

## Testing

### Unit — Python

`sdk/python/tests/unit/mod/_analyzer/test_schematool.py`:

- Object with fields + functions + constructor → correct `ObjectDef`.
- Enum with members → correct `EnumDef` + `EnumValue[]`.
- Function returning `list[Foo]` → correct `LIST(NON_NULL(OBJECT(Foo)))`
  nesting.
- Optional parameter with default → `FuncArg.DefaultValue` set.
- Private attribute (underscore-prefixed) excluded.
- Type referenced from base schema (e.g. `dagger.Container`) **not**
  emitted as a user-declared type.

### Unit — Go

`cmd/codegen/schematool/schematool_test.go` already covers
`Merge`. Add one test case that feeds a Python-generated
`ModuleTypes` JSON (fixture) into `Merge` against a canned base schema
and asserts the resulting schema contains the module's types.

### Integration

Uncomment the Python case in `core/integration/module_test.go::TestSelfCalls`
(currently lines 6446-6464). The test code is already written: a
`Test` class with a `container_echo` function and `print` /
`print_default` functions that call `dag.test().container_echo()` via
the generated client (self-call). The test is parameterized on SDK
and runs with `--with-self-calls`.

Additionally add a regression test for the **self-calls-off** path: a
Python module without `--with-self-calls`, asserting that the
generated `gen.py` does **not** contain `class Test` (since the
merge didn't run). This pins the default-off behavior so the gating
can't silently regress.

## Rollout

Patches land as independent StGit commits on top of the current branch
(19 patches including this design doc; spec 1 implementation stacks on
top):

1. `core/schema: experimentalFeatureEnabled getter on ModuleSource`
2. `sdk(python): add _analyzer emit CLI + schematool serializer`
3. `engine-dev: bundle merge-schema in python-sdk rootfs`
4. `sdk(python/runtime): self-calls gated three-phase codegen`
5. `core/integration: enable python case in TestSelfCalls`

Patch names are placeholders; the implementation plan will finalize them.

## Next specs (not in this spec)

### Spec 2 — Python SDK: static runtime entrypoint

Move all per-module code analysis from runtime into codegen. Codegen
emits a generated `entrypoint.py` with (a) pre-serialized type
declarations and (b) a static dispatch table. Runtime loads it and
dispatches without any AST walk or decorator introspection — the same
model the Go SDK uses today (`cmd/codegen/generator/go/templates/modules.go`
`mainSrc` + generated `invoke`).

**Always on** (no opt-in flag). The static path should be
behaviourally identical to the dynamic path for well-formed modules.
Runtime monkey-patching of decorators after import — e.g. conditionally
adding `@function` at runtime — stops working, matching the Go SDK's
static-compilation limitation.

Reuses spec 1's analyzer-at-codegen-time infrastructure, but runs
unconditionally (independent of `SELF_CALLS`).

### Spec 3 — Python SDK: honor `legacyCodegenAtRuntime`

After spec 2, committing `gen.py` + `entrypoint.py` becomes genuinely
zero-work-at-runtime. Spec 3 is then a mechanical port of Go PR 2:

- `pythonSDK.Codegen()` short-circuit on `legacyCodegenAtRuntime=false`
- `pythonSDK.Runtime()` bypass of `WithSDK`'s codegen exec when opted-in
- `requireGeneratedFiles(gen.py, entrypoint.py)` helper in the Python
  SDK Go-side
- `dagger init --sdk=python` writes
  `codegen.{legacyCodegenAtRuntime,automaticGitignore}`
- Integration tests for the Python opt-in
- `sdk/python/runtime/` itself (the builtin Python SDK) becomes
  legacyCodegenAtRuntime-opted-in and commits its own generated
  files under the new layout
