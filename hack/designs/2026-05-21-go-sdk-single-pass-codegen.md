# Go SDK: single-pass codegen with self-calls merge

## Status

Proposed. New patches stacked on top of the current series — nothing
existing is rewritten, so the work reverts cleanly.

## Goal

Make `codegen generate-module` run **exactly once** for a Go module,
including self-calls modules. Today it runs twice for self-calls
modules: the engine builds and runs the module binary (`asModule`) just
to discover the module's own types, then runs `generate-module` again
to produce the committed output. This design moves the self-type
discovery + merge into the single codegen pass.

Constraints (set by the user):

- Keep the existing `packages.Load`-based source analysis. Do **not**
  reintroduce a full AST-based analyzer (the `astscan` approach from
  the `no-codegen-at-runtime-go` branch).
- Do the merge via the engine's `schemaTools` (`core/schema/schematool`),
  so it is shared infrastructure other SDKs can use later.
- Land as new patches on top; do not touch existing patches.

## Why two passes exist today

For a self-calls Go module, `runCodegen` (core/schema/modulesource.go,
~line 2419) does:

```go
if srcInst.Self().SDK != nil && isSelfCallsEnabled(srcInst) {
    var mod dagql.ObjectResult[*core.Module]
    dag.Select(ctx, srcInst, &mod, dagql.Selector{Field: "asModule"})
    deps = mod.Self().Deps.Append(core.NewUserMod(mod))
}
generatedCode, err := generatedCodeImpl.Codegen(ctx, deps, srcInst)
```

`asModule` for Go (no `moduleTypes` since it was dropped) goes through
`Runtime` + empty-parentName dispatch: it runs `codegen generate-module`
in-container, `go build`, then executes the binary to read back the
module's typedefs. Those typedefs are appended to `deps`, and
`Codegen` then runs `codegen generate-module` a **second** time with
self in the schema so the generated bindings include `dag.MyModule()`.

So the cost of self-type discovery is `generate-module` + `go build` +
a binary execution, before the real `generate-module`.

For a **non-self-calls** module the `asModule` block is skipped, so
`generate-module` already runs once.

## Why single-pass is possible with `packages.Load`

The blocker people assume — "you can't analyze self-call source until
`dag.MyModule()` exists in `internal/dagger`" — does not actually hold,
because `loadPackage` strips function bodies before type-checking
(cmd/codegen/generator/go/loader.go:41-46):

```go
ParseFile: func(fset, filename, src) (*ast.File, error) {
    astFile, _ := parser.ParseFile(fset, filename, src, parser.ParseComments)
    for _, decl := range astFile.Decls {
        if fn, ok := decl.(*ast.FuncDecl); ok {
            fn.Body = nil   // strip bodies
        }
    }
    return astFile, nil
}
```

The self-call `dag.MyModule()` lives in a method **body**, which is
stripped. Type-checking only sees **signatures** (struct fields, method
params/returns), which reference module-local types (resolved from
source) or dependency types (resolved from the deps bindings already
present in `internal/dagger`). So `packages.Load` extracts the module's
own types without the self bindings present. Today's `asModule` run #1
already proves this: it analyzes self-call source against a deps-only
schema successfully.

The two-pass structure is therefore **not** about analysis — it is only
about getting self's types into the schema so the generated bindings
include `dag.MyModule()`. The codegen can do that itself.

## Target single-pass flow

Inside one `generate-module` invocation:

```
1. packages.Load → analyze user source (existing; bodies stripped)
2. emit the module's own types as introspection JSON   (NEW emitter)
3. if --self-calls:
     merged := dag.Schema(JSON(depsJSON)).Merge(moduleTypesJSON, modName).Contents(ctx)
     SetSchema(parse(merged)); SetSchemaParents(...)
4. generateCode once → dagger.gen.go (deps + self bindings) + entrypoint
   (the dispatcher's empty-parentName arm is still emitted from the same
    parsed types, unchanged)
```

Engine side: `runCodegen` stops calling `asModule` for Go. Python/TS
(which still implement `moduleTypes`) keep the engine-side self-append.

## Components

### 1. Introspection-JSON emitter (new)

The parsed types (`parsedObjectType`, `parsedIfaceType`,
`parsedEnumType`, `funcTypeSpec`, arg specs) already feed two emitters:

- `TypeDef()` (templates/typedefs.go) — builds live
  `dag.Module().WithObject(...)` via the dag client.
- `TypeDefCode()` (templates/module_objects.go etc.) — emits jen source
  for the runtime dispatcher (the empty-parentName arm).

Add a **third** emitter: parsed types → `introspection.Type` entries.

- Location: a new file under `cmd/codegen/generator/go/templates/`
  (e.g. `introspect_emit.go`), reusing the existing `visitTypes`
  visitor so it walks the same parsed structs.
- Output: a minimal `introspection.Response`/`Schema` JSON containing
  one `OBJECT`/`INTERFACE`/`ENUM` `Type` per module-defined type (with
  fields/functions and type references by name, including
  `NON_NULL`/`LIST` `ofType` trees), plus a `Query` type carrying the
  module's constructor field.
- Type references are by **name** — no live schema resolution needed.
  `schemaTools.merge` resolves them against the deps JSON and the
  module's own types. This is the same property that lets
  `TypeDefCode()` work from these structs.
- Reuse the codegen's existing type-ref formatting for the
  `NON_NULL`/`LIST`/optional shaping so the emitter does not
  re-derive it.

The prior branch produced an intermediate `schematool.ModuleTypes`
struct then converted to introspection inside its merge library. We
skip the intermediate and emit `introspection.Type` directly, because
our merge entry point (`core/schema/schematool`) already consumes
introspection JSON.

### 2. Codegen orchestration

In `cmd/codegen/generator/go/generate_module.go`, after `loadPackage`:

```go
if g.Config.ModuleConfig.SelfCalls && g.Config.Dag != nil {
    moduleTypesJSON, err := emitIntrospectionJSON(parsedTypes, moduleName)
    if err != nil {
        return nil, fmt.Errorf("emit module types for self-call merge: %w", err)
    }
    merged, err := g.Config.Dag.
        Schema(dagger.JSON(g.Config.IntrospectionJSON)).
        Merge(dagger.JSON(moduleTypesJSON), moduleName).
        Contents(ctx)
    if err != nil {
        return nil, fmt.Errorf("merge module types into schema: %w", err)
    }
    var resp introspection.Response
    if err := json.Unmarshal([]byte(merged), &resp); err != nil {
        return nil, fmt.Errorf("unmarshal merged introspection JSON: %w", err)
    }
    schema = resp.Schema
    generator.SetSchemaParents(schema)
    generator.SetSchema(schema)
}
// generateCode runs once against the (possibly merged) schema
```

This is structurally what the abandoned codegen-merge attempt did, but
using the **JSON `merge`** (which works) instead of the hollow-module
`mergeModule` (which failed because the codegen-built `dag.Module()`
had no deps to resolve `Container` etc.). The emitter feeds real
introspection JSON, so there is no module to resolve and no
hollow-module problem.

The exact dagger Go-SDK client call shape
(`dag.Schema(...).Merge(...).Contents(ctx)`) matches the regenerated
client; confirm against `sdk/go/dagger.gen.go` at implementation time.

### 3. Engine change

In `core/schema/modulesource.go` `runCodegen`, restore the
`AsModuleTypes()` gate on the self-append:

```go
if _, ok := srcInst.Self().SDKImpl.AsModuleTypes(); ok && isSelfCallsEnabled(srcInst) {
    // engine-side self-append: Python/TS (SDKs that implement moduleTypes)
    var mod dagql.ObjectResult[*core.Module]
    dag.Select(ctx, srcInst, &mod, dagql.Selector{Field: "asModule"})
    deps = mod.Self().Deps.Append(core.NewUserMod(mod))
}
```

For Go (`AsModuleTypes()` returns false), the engine no longer calls
`asModule` during codegen → no build, no binary run, no second
`generate-module`. This reverts the gate-widening that was added when
the engine-side self-append was reinstated for Go; Go now does its own
codegen-side merge.

### 4. Re-introduced plumbing

These were removed in the cleanup once the abandoned merge approach was
reverted; they now have a real consumer:

- `--self-calls` CLI flag on `generate-module` + `SelfCalls` field on
  `generator.ModuleGeneratorConfig`.
- `core/sdk/go_sdk.go` `baseWithCodegen`: add
  `experimentalPrivilegedNesting: true` to the codegen `withExec`, and
  append `--self-calls` to the codegen argv when
  `src.Self().SDK.ExperimentalFeatureEnabled(ModuleSourceExperimentalFeatureSelfCalls)`.
- The codegen container's dag connection must be available at
  `generate-module` time (the `getGlobalConfig` connect path), since
  the merge is a dagql call.

### 5. Directives

Merged types must carry:

- `@sourceModuleName` — used by the merge for idempotency
  (`moduleAlreadyMerged`) and by the engine for installed module types.
  The `merge` resolver already stamps this.
- `@sourceMap(module:)` — used by the codegen file-splitter to place
  self-call types in the correct `internal/dagger/<module>.gen.go`
  rather than the main `dagger.gen.go`. Confirm the merge stamps this;
  if not, add it (the prior branch added it specifically for the
  file-splitter).

## What this resolves

- One `generate-module` per Go module; no `go build` + binary run for
  codegen-time self-type discovery.
- `dagger generate` for a self-calls Go module no longer routes through
  `Runtime`/`asModule`, so the codegen-time chicken-and-egg (and the
  strict missing-files error during *generate*) disappears. The strict
  missing-files error remains correct for `dagger call` on a module
  whose committed generated files are absent.
- `schemaTools` finally has a production caller (the codegen-side
  merge), and remains exposed over dagql for future SDKs.

## Error handling

- Emitter: if it meets a type it cannot represent (e.g. an unsupported
  external type), fail with an error naming the type — same bar as
  `TypeDefCode`.
- Merge: name collisions surface with the existing `schemaTools`
  messages. The codegen wraps the call as
  `"merge module types into schema: %w"`.
- `SetSchemaParents` must be called after replacing the schema with the
  merged one, or template rendering nil-derefs `Field.ParentObject`
  (introspection's `ParentObject` is `json:"-"`).

## Rollout

New patches on top of the current series, each green and revertible:

1. `cmd/codegen: emit module types as introspection JSON` — the emitter
   + `@sourceMap` stamping. Additive, no behavior change (dead until
   wired).
2. `cmd/codegen + core/sdk: merge self types codegen-side via schemaTools`
   — orchestration + re-added plumbing (`--self-calls`, `SelfCalls`,
   privileged nesting, dag connection). Idempotent and safe on its own:
   the engine still self-appends at this point, so `cfg.IntrospectionJSON`
   already contains self and the codegen `merge` hits
   `moduleAlreadyMerged` (no-op).
3. `core/schema/modulesource: stop engine-side self-append for Go` —
   restore the `AsModuleTypes()` gate. Now the codegen-side merge is the
   real one and the double `generate-module` + build + run is gone for
   Go.

Plus a design-doc patch (kept local).

## Verification

After patch 3, regenerate a representative self-calls Go module and
confirm:

- `generate-module` runs once — no `asModule` build/run span in the
  trace.
- The generated `dagger.gen.go` contains the `dag.MyModule()` self
  binding and compiles.
- The runtime empty-parentName dispatch still answers for `dagger call`.
- Non-self-calls Go modules are unaffected (already single-pass).
- Python/TS modules are unaffected (still use engine-side self-append).

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Emitter's introspection JSON diverges from what engine `asModule` produced (optionality, list nesting, enum values, directives), so self bindings differ | Medium | Parity check during implementation: diff the merged schema from the codegen path vs the engine `asModule` path for a representative self-calls module (object with functions, interface, enum, constructor, self-referential return). They must match. |
| File-splitter misplaces self-call types if `@sourceMap` is missing | Medium | Explicit check that the merge stamps `@sourceMap`; add it if absent. |
| Re-adding `experimentalPrivilegedNesting` changes the codegen cache key | High once, then stable | Accepted one-time cache miss; note in the patch. |
| `packages.Load` fails to extract some type because a signature (not body) references an unresolved dependency type | Low — deps bindings are always present | Surfaces as a clear analysis error; same behavior as today. |

## Non-goals

- No AST-based analyzer (`astscan`). `packages.Load` stays.
- No change to non-Go SDKs.
- No change to the runtime no-codegen path
  (`legacyCodegenAtRuntime=false`) or its strict missing-files contract
  for `dagger call`.
- No removal of the runtime empty-parentName dispatch — it remains how
  the engine discovers types at module-load/execution time.
