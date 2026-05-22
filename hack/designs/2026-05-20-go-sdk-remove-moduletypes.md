# Go SDK: remove `moduleTypes`

## Status

Proposed. Stacks on top of `go-sdk-runtime-skip-codegen` (the runtime
patch that just landed). Three stg patches.

## Goal

Delete the Go SDK's `ModuleTypes` SDK call — the `codegen
generate-typedefs` subprocess invocation that runs in a privileged
container at module load. Type discovery folds back into the existing
`generate-module` codegen pass; the resulting `dagger.gen.go`
dispatcher handles `parentName == ""` itself by returning a
`*dagger.Module` built from baked-in static type info. Self-calls keep
working via the engine's existing `schemaTools`, called from codegen.

`packages.Load` stays — no AST switch. The deliberately scary, large
"replace source analysis" change in the experimentation branch is out
of scope. We restore the pre-`moduleTypes`-split dispatcher pattern
(commit `44c9087a0` in main split it; commit `2ef3014e2` in the
experimentation branch restored it on top of the AST work) and keep the
existing analysis machinery as-is.

## Scope

In scope:

- Go SDK only. Other SDKs (Python, TS, …) keep `AsModuleTypes()` and
  continue to use the engine's `moduleTypes` dispatch unchanged.
- Restore `parentName == ""` dispatch in the Go SDK's generated
  `dagger.gen.go` (`TypeDefCode()` methods on parsed types + new
  dispatcher arm).
- Codegen-side schema merge for self-calls via the engine's existing
  `schemaTools` (dagql call), with a small `Schema.MergeModule(modID)`
  extension so codegen hands the engine a Module ID and lets the engine
  do the introspection-JSON conversion.
- Flip Go SDK's `AsModuleTypes()` to `(nil, false)`, delete the Go
  SDK's `ModuleTypes` method body.

Out of scope (deferred):

- AST-based source analysis. KISS — keep `packages.Load`.
- Touching other SDKs.
- Removing the `ModuleTypes` interface from `core/sdk.go` or any
  engine-side `moduleTypes` dispatch — they stay alive for other SDKs.
- Deleting the `generate-typedefs` subcommand / template — stops being
  called by the Go SDK but the code stays for now (trivial follow-up
  to remove).
- `core/integration` tests. Per existing pattern in this branch — land
  in a follow-up patch.
- Anything in `ClientGenerator` / `generate-client`.

## Background

Before PR #10584 (commit `44c9087a0`), the Go SDK's `generate-module`
template emitted a `register` function that the dispatcher called when
`parentName == ""`, returning a `*dagger.Module` built from typedefs
extracted at codegen time. PR #10584 split type discovery out into a
separate `moduleTypes` SDK call — a second container invocation
running `codegen generate-typedefs` — to "expose module type
definitions" as a first-class SDK operation.

The split simplified some SDK plumbing but added a runtime cost: a
second container (plus `experimentalPrivilegedNesting` for dagql
access) runs on every module load. Removing the split brings type
discovery back into the same codegen pass that already produces the
bindings.

## Current state

`core/sdk/go_sdk.go`:

- `AsModuleTypes()` returns `(sdk, true)` — engine routes through
  `moduleTypes` (line ~57 today).
- `ModuleTypes()` (line ~250–~440) builds a container, runs `codegen
  generate-typedefs --output typedefs.json` with privileged nesting,
  reads back the resulting JSON, deserializes a `core.ModuleID`,
  resolves it through dagql, returns the loaded `*core.Module`.

`core/schema/modulesource.go`:

- `runModuleDefInSDK` (line ~2825): if `AsModuleTypes()` says yes, call
  `typeDefsImpl.ModuleTypes(...)`. Otherwise fall through to
  `Runtime + empty-parentName call` (line ~2833–~2887).
- `runGeneratedCodeDirectory` (line ~2519) and `initializeSDKModule`
  (line ~3097): `if AsModuleTypes() && isSelfCallsEnabled → mod.Deps =
  mod.Deps.Append(mod)` (self-append into deps for self-calls
  modules).

`cmd/codegen/generator/go/generate_typedefs.go`: the `generate-typedefs`
subcommand. Uses `packages.Load` + the `TypeDefs()` template (in
`templates/typedefs.go`) to build `dag.Module().WithObject(...).ID()`
inside the privileged container and return the JSON.

`cmd/codegen/generator/go/generate_module.go`: the `generate-module`
subcommand. Uses `packages.Load` + the `GoModule` template (in
`templates/modules.go`) to emit `dagger.gen.go` with `dispatch()` and
`invoke()` — no `parentName == ""` arm today.

`core/schematool.go` + `core/schema/schematool.go`: engine-side schema
inspect + merge tools exposed over dagql:
`dag.schema(json).{listTypes,hasType,describeType,merge,contents}`.
The `merge` method takes introspection-shaped JSON. No `mergeModule`
variant yet.

## Target state

```
dagger call foo (Go SDK, after this series):

asModule (engine)
  AsModuleTypes() → (nil, false)
  ──> Runtime(deps, source)  (no moduleTypes container, no self-append)
       container.withExec([], parentName="")
         dispatcher's case "" returns dag.Module().WithObject(...).ID()
           ↑ types baked at codegen time via TypeDefCode() jen-emit

dagger develop (Go SDK, after this series)
  Codegen(deps, source)
    codegen container runs generate-module (privileged nesting now)
    packages.Load → parseState (unchanged)
    if --self-calls:
      build dag.Module().WithObject(...) from parsedTypes
      modID := dag.Module(...).ID()
      mergedJSON := dag.Schema(depsJSON).MergeModule(modID).Contents()
      use mergedJSON for binding generation
    else:
      use depsJSON unchanged
    emit dagger.gen.go (now includes case "" arm + bindings)
```

Engine's `moduleTypes` dispatch path stays for non-Go SDKs.

## Patch decomposition

Three stg patches. Each leaves the tree green and is independently
revertable in the obvious way.

### Patch A — `go-sdk-empty-parentname-dispatch`

Re-derive the experimentation branch's commit `2ef3014e2`
("cmd/codegen/generator/go: restore TypeDefCode for empty-parentName
dispatch"). Template-only, +341 lines.

| File | Change |
|---|---|
| `cmd/codegen/generator/go/templates/module_funcs.go` | `voidDef` jen helper; `TypeDefCode()` method on `funcTypeSpec` + arg-spec helpers. Emits the jen for `dag.Function(name, returnType).WithArg(...)`. |
| `cmd/codegen/generator/go/templates/module_objects.go` | `TypeDefCode()` on `parsedObjectType`. Emits `dag.TypeDef().WithObject("Name").WithFunction(...)`. |
| `cmd/codegen/generator/go/templates/module_interfaces.go` | `TypeDefCode()` on `parsedIfaceType`. |
| `cmd/codegen/generator/go/templates/module_enums.go` | `TypeDefCode()` on `parsedEnumType`. |
| `cmd/codegen/generator/go/templates/module_types.go` | `TypeDefCode()` on basic type-spec types so child types can be emitted. |
| `cmd/codegen/generator/go/templates/modules.go` (location of `invokeSrc`) | Teach `invokeSrc` to emit, after the per-object switch arms, a `case "":` arm calling a generated helper that returns `dag.Module().WithObject(...).WithInterface(...).WithEnum(...).ID(ctx)`. |

After Patch A, every freshly-regenerated `dagger.gen.go` has the new
arm. **Engine still routes through `moduleTypes` for Go modules**, so
the arm is dead code from the engine's perspective for now. The
generated bindings build, vet, and pass existing tests.

Verification:

- `go build ./cmd/codegen/...` clean.
- `go test ./cmd/codegen/...` passes (existing template tests).
- Smoke: regenerate `core/integration/testdata/modules/go/defaults/dagger.gen.go`
  in a scratch directory, grep for the new `case "":` arm. Do not commit
  the regenerated fixture.

### Patch B — `go-sdk-codegen-self-calls-merge`

Make codegen do the schema merge for self-calls modules using the
engine's `schemaTools`, with a small `MergeModule(modID)` extension.

| File | Change |
|---|---|
| `core/schematool.go` | Add `func (s *Schema) MergeModule(ctx, dag, modID dagql.ID[*Module]) (*Schema, error)`. Loads the module, runs the engine's existing Append + serialize trick to derive introspection JSON for *just* this module, calls existing `s.Merge(json, mod.Name())`. ~30 lines reusing existing logic. |
| `core/schema/schematool.go` | Add `dagql.Func("mergeModule", s.mergeModule)` field on `*core.Schema` with doc + a 5-line resolver wrapping `Schema.MergeModule`. |
| `cmd/codegen/generate_module.go` | Flip `getGlobalConfig(ctx, true)` so the codegen container's `dagger.Connect(ctx)` always returns a usable dag — required for the new `MergeModule` call. |
| `cmd/codegen/generator/go/generate_module.go` | If `--self-calls` flag is set: walk `parseState` parsed types, build a `dag.Module().WithObject(...).WithInterface(...).WithEnum(...)` shape (same Go-level shape Patch A's `TypeDefCode` jen-emits at runtime), get `ID()`, then `dag.Schema(depsJSON).MergeModule(modID).Contents()`, and use that merged JSON for binding generation. |
| `cmd/codegen/generator/go/generate_module.go` | Add the `--self-calls` CLI flag wiring. |
| `core/sdk/go_sdk.go` (`baseWithCodegen`) | Add `experimentalPrivilegedNesting: true` to the codegen `withExec` selector so the codegen container's dagql client works. |
| `core/sdk/go_sdk.go` (`baseWithCodegen`) | When `src.SDK.ExperimentalFeatureEnabled(core.ModuleSourceExperimentalFeatureSelfCalls)` is true, append `--self-calls` to the codegen argv. |

After Patch B: engine still appends self to deps (Patch C handles
that). The merged JSON the codegen sees already contains self, so
`Schema.Merge`'s `moduleAlreadyMerged` short-circuits and returns the
schema unchanged. **The merge is functionally a no-op until Patch C
lands**, and that idempotency is what makes the patch safe to ship
alone.

`Schema.MergeModule` sketch:

```go
// MergeModule merges a Module's types into the schema, returning the
// combined schema. Equivalent to Merge but takes a Module ID rather
// than introspection-shaped JSON — the engine derives the JSON from
// the module internally, so codegen-side conversion isn't needed.
func (s *Schema) MergeModule(
    ctx context.Context,
    dag *dagql.Server,
    modID dagql.ID[*Module],
) (*Schema, error) {
    mod, err := modID.Load(ctx, dag)
    if err != nil {
        return nil, fmt.Errorf("load module: %w", err)
    }
    // Use the engine's existing SchemaBuilder Append + serialize path
    // — same code engine self-append uses today — to derive the
    // module's introspection JSON.
    builder := /* construct a builder containing just mod */
    if err := builder.Append(ctx, mod.Self()); err != nil {
        return nil, fmt.Errorf("append module to builder: %w", err)
    }
    jsonFile, err := builder.SchemaIntrospectionJSONFileForModule(ctx)
    if err != nil {
        return nil, fmt.Errorf("serialize module schema: %w", err)
    }
    contents, err := jsonFile.Self().Contents(ctx)
    if err != nil {
        return nil, fmt.Errorf("read module schema JSON: %w", err)
    }
    return s.Merge(JSON(contents), mod.Self().Name())
}
```

The exact `SchemaBuilder` constructor + `Append` signature is
finalized during Patch B implementation — if there's a simpler
single-module helper already on `SchemaBuilder` or `Module`, we use
it. The contract above is the conceptual shape; the engine's existing
conversion logic is the source of truth either way.

Verification:

- `go build ./...` clean.
- `go test ./core/... ./cmd/codegen/...` passes.
- Manual smoke: build a non-self-calls Go module with the patch
  applied; confirm codegen runs without error (validates the
  privileged-nesting wiring even though MergeModule isn't called).

### Patch C — `go-sdk-drop-moduletypes`

The flip.

| File | Change |
|---|---|
| `core/sdk/go_sdk.go` | `AsModuleTypes()` returns `nil, false` (with a comment naming the empty-parentName fallback). |
| `core/sdk/go_sdk.go` | Delete the entire `func (sdk *goSDK) ModuleTypes(...)` body (~250 lines, `core/sdk/go_sdk.go:250` to ~`449`). |
| `core/sdk/go_sdk.go` | Drop now-unused imports (`encoding/json`) and constants (`GoSDKModuleIDPath`, `goSDKExecMDDigest`) iff nothing else in the file references them. |

After Patch C: engine sees `AsModuleTypes() = (nil, false)` for Go
modules and:

1. Skips the `moduleTypes` branch in `runModuleDefInSDK` → falls
   through to `Runtime + empty-parentName` (engine code at line
   ~2833). Patch A's dispatcher answers.
2. Skips `mod.Deps = mod.Deps.Append(mod)` in
   `runGeneratedCodeDirectory` and `initializeSDKModule`. Codegen
   receives deps-without-self. Patch B's merge becomes the only
   source of merged JSON.

Verification:

- `go build ./...` clean.
- `go test ./core/... ./cmd/codegen/...` passes.
- Manual smoke: a small Go module with and without self-calls,
  exercise `dagger call` against both, confirm the same observed
  behavior as pre-series.

## Error handling

- **Patch A:** `dag.Module().WithObject(...).ID(ctx)` errors at runtime
  bubble through `invoke()` → `dispatch()` →
  `fnCall.ReturnError(...)`. Same path user-function errors take
  today. No new sinks.
- **Patch B `MergeModule`:** wrap caller side as
  `"merge module into schema for self-calls: %w"`. Engine-side
  failures (load, append, serialize) wrap with the prefixes shown in
  the sketch. The rare "type already exists" collision surfaces
  unchanged from `Schema.Merge`.
- **Patch B dag.Connect:** flipping `getGlobalConfig(ctx, true)` for
  `generate-module` makes the connection mandatory. If
  `experimentalPrivilegedNesting` isn't wired correctly the connect
  fails with the existing "failed to connect to engine" message.
- **Patch C fallback:** the engine's existing `runModuleDefInSDK`
  empty-parentName branch already wraps its errors (`"failed to call
  module %q to get functions: %w"`). No new wrapping needed.

## Cache behavior

Adding `experimentalPrivilegedNesting: true` to `baseWithCodegen`'s
exec changes the buildkit cache key for the codegen step. One-time
cache miss across users' first re-codegen after upgrade. Flagged in
Patch B's commit message. No mitigation needed.

Removing the `moduleTypes` container exec (Patch C) means one fewer
buildkit step on the module-load hot path. The engine's
empty-parentName fallback uses standard cache behavior — same inputs
produce the same `ModuleID`, naturally cached without the
`CallDigest`/`execMD` trickery the deleted `moduleTypes` path used.
Accepted: no measured regression yet, and per design discussion the
KISS path wins here.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| `Schema.MergeModule` produces a JSON shape subtly different from engine's `SchemaBuilder.Append`, causing self-calls modules to silently differ from today | Low — uses the same engine code path internally | During Patch B implementation, diff `dagger.gen.go` produced under engine self-append (current) vs codegen MergeModule (new) for a small self-calls module. They must match byte-for-byte. |
| Re-derived template code from branch commit `2ef3014e2` rebases differently against our `packages.Load` setup vs the branch's AST setup | Medium | The branch's commit was a *restoration* of pre-PR-#10584 code; our branch retains the same `packages.Load`-based `parseState`. Re-derive by reading the branch diff file-by-file rather than mechanical cherry-pick. |
| Patch C lands on a stack where A or B is missing → runtime failure on module load | Critical | Stack order enforces this. Re-confirm `stg series` and run smoke build before refreshing C. |
| Adding `experimentalPrivilegedNesting` to `baseWithCodegen` regresses the security posture | Low — `moduleTypes` already has privileged nesting today, this is moving the privilege rather than expanding it | Net change in privileged execs across the series: zero. Patch B grants codegen privileged nesting; Patch C removes it from `moduleTypes` (which itself is deleted entirely, taking its privileged-nesting exec with it). |
| Self-calls in a Go module trigger `MergeModule` against a partial introspection JSON missing a transitive dep type | Low | If hit, surfaces as a clean "type X already exists" or "type Y not found" error during codegen, not a silent failure. Fix forward. |
| One of the patches leaves dead unused code (e.g., `TypeDefCode` after Patch A but before Patch C) that triggers lint warnings | Low — Go tolerates unused unexported functions | If a linter complains, suppress via a comment in Patch A; the dead-code state is temporary. |

## Rollout

Three stg patches in order on the current top (`go-sdk-runtime-skip-codegen`):

```
+ legacy-codegen-flag
+ go-sdk-skip-codegen-runtime-design
+ go-sdk-skip-codegen-runtime-plan
+ go-sdk-runtime-skip-codegen
+ go-sdk-empty-parentname-dispatch          (Patch A — ~341 line additions)
+ go-sdk-codegen-self-calls-merge           (Patch B — ~60 line additions)
> go-sdk-drop-moduletypes                   (Patch C — ~250 line deletions)
! schematool-library
```

Each patch's commit message names the next/previous patch in the
series so a reviewer reading one can trace the others.

## Non-goals reaffirmed

- Not switching to AST-based source analysis.
- Not deleting `generate-typedefs` subcommand / template.
- Not deleting `core/sdk.go`'s `ModuleTypes` interface or
  engine-side `moduleTypes` dispatch.
- Not adding integration tests.
- Not changing `ClientGenerator` / client generation paths.
- Not touching other SDKs.
