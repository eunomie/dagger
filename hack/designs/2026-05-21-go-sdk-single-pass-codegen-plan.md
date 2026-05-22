# Single-pass Go codegen with self-calls merge — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `codegen generate-module` run exactly once for a Go module (today it runs twice for self-calls modules: engine `asModule` build+run to discover self's types, then a second `generate-module`). Move self-type discovery + merge into the single codegen pass.

**Architecture:** Add a third emitter to the Go codegen that turns the existing `packages.Load`-parsed types into introspection JSON. When self-calls is enabled, `generate-module` merges that JSON into the deps introspection schema via the engine's `schemaTools` (`dag.schema(deps).merge(...)`), then generates once. The engine stops doing the codegen-time `asModule` self-append for Go (Python/TS keep it). `packages.Load` is retained — no AST analyzer.

**Tech Stack:** Go, `golang.org/x/tools/go/packages`, `cmd/codegen/introspection`, dagql, the dagger Go SDK client, StGit.

**Reference:** spec at `hack/designs/2026-05-21-go-sdk-single-pass-codegen.md`.

---

## Prerequisites

- Working dir: `/home/yves/dev/src/github.com/dagger/dagger-worktrees/workspace-go-no-codegen-at-runtime`
- Branch: `workspace-go-no-codegen-at-runtime`
- Stack top must be `go-sdk-single-pass-codegen-design`. Verify with `stg series --all`.
- New patches only — do not edit existing patches. Each patch is created with `stg new` then `stg refresh`.
- Never `git commit`. Never `git push`. No `Co-Authored-By`.
- Untracked `.claude/` files must not be staged.

Baseline green check before starting:

```bash
go build ./core/... ./cmd/codegen/... && go vet ./cmd/codegen/... && go test ./cmd/codegen/...
```

---

## Key reference points (read before implementing)

- The parsed-type structs + their two existing emitters live in `cmd/codegen/generator/go/templates/`:
  - `module_objects.go` — `parsedObjectType` (`.name`, `.fields []*fieldSpec`, `.methods []*funcTypeSpec`, `.constructor *funcTypeSpec`, `.doc`, `.deprecated`, `.sourceMap`); has `TypeDefCode()` and `TypeDef(dag)`.
  - `module_interfaces.go` — `parsedIfaceType` (`.name`, `.methods`); `TypeDef(dag)`.
  - `module_enums.go` — `parsedEnumType` + `parsedEnumTypeReference`; `TypeDef(dag)`.
  - `module_funcs.go` — `funcTypeSpec` (`.name`, `.argSpecs []*paramSpec`, `.returnSpec ParsedType`, `.doc`); `paramSpec` (`.name`, `.typeSpec ParsedType`, `.optional`, `.defaultValue`, `.isContext`); `TypeDef(dag)` + `TypeDefFunc(dag)`.
  - `module_types.go` — `ParsedType` interface (`.TypeDef(dag)`, `.TypeDefCode()`, `.GoType()`, `.GoSubTypes()`), leaf types `parsedPrimitiveType`, `parsedSliceType`, `parsedObjectTypeReference`, `parsedIfaceTypeReference`; the `NamedParsedType` interface (`.Name()`, `.ModuleName()`).
- The introspection types live in `cmd/codegen/introspection/introspection.go`:
  - `Type{Kind TypeKind, Name string, Description string, Fields []*Field, EnumValues []EnumValue, Interfaces []*Type, Directives Directives}`
  - `Field{Name string, Description string, TypeRef *TypeRef, Args InputValues, Directives Directives}`
  - `TypeRef{Kind TypeKind, Name string, OfType *TypeRef}`
  - `InputValue{Name string, Description string, DefaultValue *string, TypeRef *TypeRef}`
  - `EnumValue{Name string, Description string}`
  - `Response{Schema *Schema, ...}`, `Schema{Types Types, QueryType *TypeRef, ...}` — check exact field names/json tags in the file.
  - `TypeKind` constants: `TypeKindObject`, `TypeKindInterface`, `TypeKindEnum`, `TypeKindScalar`, `TypeKindList`, `TypeKindNonNull`, `TypeKindInputObject`.
- The merge entry point: `core/schematool.go` `Schema.Merge(moduleTypes JSON, moduleName string)` — parses `moduleTypes` via `parseIntrospectionResponse` (requires a `__schema`), appends every `isModuleDefinedType` Type, stamps `@sourceModuleName`, and runs `mergeQueryConstructor` which reads the module's `Query` type for the constructor field. Read this function fully before Task 1.4 so the emitted JSON matches what it expects.
- The dagql wrapper: `core/schema/schematool.go` exposes `dag.schema(json:).merge(moduleTypes:, moduleName:)`.
- The regenerated Go client: `sdk/go/dagger.gen.go` exposes `(*Query).Schema(json JSON) *Schema`, `(*Schema).Merge(moduleTypes JSON, moduleName string) *Schema`, `(*Schema).Contents(ctx) (JSON, error)`. Confirm these signatures before Task 2.4.
- Engine codegen-time self-append: `core/schema/modulesource.go` `runCodegen` (~line 2419-2440).

---

# PATCH 1 — introspection-JSON emitter

Goal: a new emitter that converts parsed types to introspection JSON. Purely additive; nothing calls it yet, so behavior is unchanged. Validated entirely by unit/golden tests.

## Task 1.1: Create the empty stg patch

- [ ] **Step 1: Verify baseline**

```bash
stg series --all | tail -3
```
Expected: top is `go-sdk-single-pass-codegen-design`.

- [ ] **Step 2: Create patch**

```bash
stg new go-sdk-codegen-introspection-emitter -m "WIP emitter

Signed-off-by: Yves Brissaud <yves@dagger.io>"
```

## Task 1.2: TypeRef emitter for leaf/reference types

**Files:**
- Create: `cmd/codegen/generator/go/templates/introspect_emit.go`
- Create: `cmd/codegen/generator/go/templates/introspect_emit_test.go`

The emitter mirrors the existing `TypeDef(dag)` tree but builds `*introspection.TypeRef`. Optionality rule (mirrors `TypeDef` + `WithOptional`): a non-pointer scalar/object is wrapped in `NON_NULL`; a pointer is left nullable (no `NON_NULL`); a slice is `NON_NULL{ LIST{ <elem> } }` for a non-pointer slice. Match exactly what the engine produces — the parity check in Task 4 is the gate.

- [ ] **Step 1: Write the failing test**

```go
package templates

import (
	"testing"

	"github.com/dagger/dagger/cmd/codegen/introspection"
	"github.com/stretchr/testify/require"
)

func TestIntrospectTypeRef_Primitive(t *testing.T) {
	// non-pointer string -> NON_NULL{SCALAR String}
	ref := introspectTypeRef(&parsedPrimitiveType{goType: stringBasic(t)})
	require.Equal(t, introspection.TypeKindNonNull, ref.Kind)
	require.Equal(t, introspection.TypeKindScalar, ref.OfType.Kind)
	require.Equal(t, "String", ref.OfType.Name)
}
```

(Add a `stringBasic(t)` test helper returning `types.Typ[types.String]` wrapped as the leaf the codegen uses; mirror how existing template tests construct `parsedPrimitiveType` — see `module_types.go` tests / `helper_test.go`.)

- [ ] **Step 2: Run, expect FAIL (undefined: introspectTypeRef)**

```bash
go test ./cmd/codegen/generator/go/templates/ -run TestIntrospectTypeRef -v
```

- [ ] **Step 3: Implement `introspectTypeRef`**

Create `introspect_emit.go`. Implement `introspectTypeRef(spec ParsedType) *introspection.TypeRef` covering each leaf, mirroring the corresponding `TypeDef(dag)` body:

- `*parsedPrimitiveType`: map `goType.Info()` → scalar name (`String`/`Int`/`Boolean`/`Float`) exactly as `TypeDef` maps to `TypeDefKind*Kind` (module_types.go:201-232). Scalar custom types (`spec.scalarType != nil`) → `SCALAR` with the scalar's name. Wrap in `NON_NULL` unless `spec.isPtr`.
- `*parsedSliceType`: `NON_NULL{ LIST{ introspectTypeRef(spec.underlying) } }` (the engine wraps non-optional lists in NON_NULL; confirm against parity check).
- `*parsedObjectTypeReference`: `NON_NULL{ OBJECT name }` unless `spec.isPtr` → `OBJECT name`. Name via `typeName`-less form: use `spec.name` (module-local) — the merge resolves by name.
- `*parsedIfaceTypeReference`: same as object-ref but `INTERFACE`.
- `*parsedEnumTypeReference`: `NON_NULL{ ENUM name }` (enums are value types; confirm optionality against parity check).

Use a type switch on `ParsedType`. Return a clear error path is not needed here (return the ref); for an unrepresentable type, panic-free: return a `NON_NULL{SCALAR "Void"}`-style fallback ONLY if the existing `TypeDef` did (it maps invalid → Void). Otherwise add a sibling `introspectTypeRefErr(spec) (*introspection.TypeRef, error)` if you need to surface "unsupported type" — match the `TypeDef` error behavior.

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./cmd/codegen/generator/go/templates/ -run TestIntrospectTypeRef -v
```

- [ ] **Step 5: Add cases** for pointer (nullable, no NON_NULL), slice, object-ref, iface-ref, enum-ref. Each as its own `require`-based subtest. Run until green.

- [ ] **Step 6: `stg refresh`**

## Task 1.3: Type emitter for object / interface / enum

**Files:**
- Modify: `cmd/codegen/generator/go/templates/introspect_emit.go`
- Modify: `cmd/codegen/generator/go/templates/introspect_emit_test.go`

- [ ] **Step 1: Write failing tests** for `introspectObject`, `introspectInterface`, `introspectEnum`. Example for object:

```go
func TestIntrospectObject_WithFunction(t *testing.T) {
	obj := /* construct a parsedObjectType named "Test" with one method
	          Echo(s string) string — mirror how module_objects.go tests build it */
	it := introspectObject(obj)
	require.Equal(t, introspection.TypeKindObject, it.Kind)
	require.Equal(t, "Test", it.Name)
	require.Len(t, it.Fields, 1)
	f := it.Fields[0]
	require.Equal(t, "echo", f.Name)                       // lowerCamel field name
	require.Equal(t, "String", f.TypeRef.OfType.Name)      // NON_NULL{String}
	require.Len(t, f.Args, 1)
	require.Equal(t, "s", f.Args[0].Name)
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement the three Type emitters.** Mirror the existing `TypeDef(dag)` methods one-to-one, building `*introspection.Type`:

  - `introspectObject(spec *parsedObjectType) *introspection.Type` mirrors `parsedObjectType.TypeDef` (module_objects.go:256). For each method (`funcTypeSpec`) emit an `introspection.Field`: name = lowerCamel of method name; `TypeRef = introspectTypeRef(spec.returnSpec)`; `Args` from `argSpecs` (skip `isContext`); each arg → `InputValue{Name, TypeRef: introspectTypeRef(argSpec.typeSpec), DefaultValue}` with optionality from `argSpec.optional`. Include explicit `.fields` (struct fields) the same way `TypeDef` does. Include the constructor as a field on `Query` later (Task 1.4), not here.
  - `introspectInterface(spec *parsedIfaceType) *introspection.Type` mirrors `parsedIfaceType.TypeDef` (module_interfaces.go:133): `Kind=INTERFACE`, methods → `Fields`.
  - `introspectEnum(spec *parsedEnumType) *introspection.Type` mirrors `parsedEnumType.TypeDef`: `Kind=ENUM`, members → `EnumValues`.

  Carry `Description` from `.doc` and deprecation/sourceMap as the engine does (check `TypeDef`); the parity check validates fidelity.

- [ ] **Step 4: Run, expect PASS.** Add interface + enum subtests. Run until green.

- [ ] **Step 5: `stg refresh`**

## Task 1.4: Top-level `ModuleIntrospectionJSON` + Query constructor + directives

**Files:**
- Modify: `cmd/codegen/generator/go/templates/introspect_emit.go`
- Modify: `cmd/codegen/generator/go/templates/introspect_emit_test.go`

- [ ] **Step 1: Read `core/schematool.go` `Merge` + `mergeQueryConstructor` fully.** Confirm: it reads `module.Schema.Types` for module-defined types, and `module.Schema.Query()` to find the constructor field named `strcase.ToLowerCamel(moduleName)`. The emitted JSON must contain a `Query` type carrying that field, returning `NON_NULL{OBJECT <MainObject>}`.

- [ ] **Step 2: Write the failing test**

```go
func TestModuleIntrospectionJSON_RoundTripsThroughMerge(t *testing.T) {
	// Build a goTemplateFuncs/visitor result with one object "Test" (the main object)
	// + one method. Produce JSON, parse as introspection.Response, assert:
	//   - Types contains "Test"
	//   - Query type has field "test" returning NON_NULL{OBJECT Test}
	jsonBytes := mustModuleIntrospectionJSON(t, /* parsed types */, "test")
	var resp introspection.Response
	require.NoError(t, json.Unmarshal(jsonBytes, &resp))
	require.NotNil(t, resp.Schema.Types.Get("Test"))
	q := resp.Schema.Types.Get("Query")
	require.NotNil(t, q)
	// find field "test"
	...
}
```

- [ ] **Step 3: Implement `ModuleIntrospectionJSON`**

```go
// ModuleIntrospectionJSON walks the module's parsed types (via the same
// visitor the dispatcher uses) and emits a minimal introspection
// Response containing the module's object/interface/enum types plus a
// Query type carrying the module's constructor field. The output is the
// `moduleTypes` argument to schemaTools.merge.
func (funcs goTemplateFuncs) ModuleIntrospectionJSON(moduleName string) ([]byte, error) {
	types := introspection.Types{}
	var mainObject *parsedObjectType
	err := funcs.visitTypes(true, &visitorFuncs{
		StructVisitor: func(_ *parseState, _ *types.Named, obj *types.TypeName, spec *parsedObjectType, _ *types.Struct) error {
			types = append(types, introspectObject(spec))
			if strcase.ToCamel(spec.name) == strcase.ToCamel(moduleName) {
				mainObject = spec
			}
			return nil
		},
		IfaceVisitor: func(_ *parseState, _ *types.Named, _ *types.TypeName, spec *parsedIfaceType, _ *types.Interface) error {
			types = append(types, introspectInterface(spec))
			return nil
		},
		EnumVisitor: func(_ *parseState, _ *types.Named, _ *types.TypeName, spec *parsedEnumType, _ *types.Basic) error {
			types = append(types, introspectEnum(spec))
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("visit module types: %w", err)
	}
	queryType := &introspection.Type{Kind: introspection.TypeKindObject, Name: "Query"}
	if mainObject != nil {
		ctorRef := &introspection.TypeRef{
			Kind:   introspection.TypeKindNonNull,
			OfType: &introspection.TypeRef{Kind: introspection.TypeKindObject, Name: mainObject.name},
		}
		field := &introspection.Field{Name: strcase.ToLowerCamel(moduleName), TypeRef: ctorRef}
		if mainObject.constructor != nil {
			field.Args = introspectArgs(mainObject.constructor) // reuse arg emission
		}
		queryType.Fields = append(queryType.Fields, field)
	}
	types = append(types, queryType)
	resp := introspection.Response{Schema: &introspection.Schema{Types: types}}
	return json.Marshal(resp)
}
```

(Adjust `introspection.Schema` field names / required fields — e.g. `QueryType`, `MutationType` — to whatever `parseIntrospectionResponse` requires; the round-trip test catches mismatches. `introspectArgs` is a small helper factored out of Task 1.3's method-arg emission.)

- [ ] **Step 4: Run, expect PASS.** Run until green.

- [ ] **Step 5: Note on directives.** `schemaTools.merge` stamps `@sourceModuleName` on appended types. Verify whether it also stamps `@sourceMap(module:)` (needed by the codegen file-splitter to place self types in `internal/dagger/<module>.gen.go`). Check `core/schematool.go` `sourceModuleDirective` / the merge body. If `@sourceMap` is missing, that's a Patch 2 follow-up (note it; do not fix here).

- [ ] **Step 6: `stg refresh`**

## Task 1.5: Verify + finalize Patch 1

- [ ] **Step 1:**
```bash
go build ./cmd/codegen/... && go vet ./cmd/codegen/... && go test ./cmd/codegen/generator/go/templates/ -run 'Introspect|ModuleIntrospection' -v
```
Expected: all pass.

- [ ] **Step 2: `stg refresh` then finalize message**

```bash
stg edit -m "cmd/codegen/generator/go: emit module types as introspection JSON

Add a third emitter alongside TypeDef() and TypeDefCode(): walk the
packages.Load-parsed module types and produce a minimal introspection
Response (the module's object/interface/enum types + a Query
constructor field), with type references by name. This is the
moduleTypes input for schemaTools.merge.

Purely additive: no caller yet, behavior unchanged. Covered by unit
+ round-trip tests.

Signed-off-by: Yves Brissaud <yves@dagger.io>"
```

---

# PATCH 2 — codegen-side merge via schemaTools

Goal: wire the emitter into `generate-module` so self-calls modules merge their types into the deps schema via the engine's `schemaTools`. Safe on its own: the engine still self-appends at this point, so the merge is idempotent (`moduleAlreadyMerged` → no-op).

## Task 2.1: Create patch

- [ ] `stg new go-sdk-codegen-self-calls-merge-onepass -m "WIP merge\n\nSigned-off-by: Yves Brissaud <yves@dagger.io>"`

## Task 2.2: Re-add the `--self-calls` flag + config field

**Files:**
- Modify: `cmd/codegen/generate_module.go`
- Modify: `cmd/codegen/generator/config.go`

- [ ] **Step 1:** In `cmd/codegen/generator/config.go`, add to `ModuleGeneratorConfig`:
```go
	// SelfCalls indicates the module has the self-calls experimental
	// feature enabled. When true, generate-module merges the module's
	// own types into the introspection schema before generating bindings.
	SelfCalls bool
```

- [ ] **Step 2:** In `cmd/codegen/generate_module.go`: add package var `selfCalls bool`; register `generateModuleCmd.Flags().BoolVar(&selfCalls, "self-calls", false, "merge the module's own types into the schema for self-call bindings")` in `init()`; set `SelfCalls: selfCalls` in the `ModuleGeneratorConfig` literal.

- [ ] **Step 3:** Ensure the codegen has a dag connection at `generate-module` time. Set `getGlobalConfig(ctx, true)` in `GenerateModule` (it was `false`). The merge is a dagql call needing `cfg.Dag`.

- [ ] **Step 4:** `go build ./cmd/codegen/...` (clean). `stg refresh`.

## Task 2.3: Orchestrate the merge in `GenerateModule`

**Files:**
- Modify: `cmd/codegen/generator/go/generate_module.go`

- [ ] **Step 1:** After `loadPackage` succeeds and the schema is available, before the final `generateCode`, insert:

```go
if g.Config.ModuleConfig.SelfCalls && g.Config.Dag != nil {
	moduleTypesJSON, err := genFuncs.ModuleIntrospectionJSON(g.Config.ModuleConfig.ModuleName)
	if err != nil {
		return nil, fmt.Errorf("emit module types for self-call merge: %w", err)
	}
	merged, err := g.Config.Dag.
		Schema(dagger.JSON(g.Config.IntrospectionJSON)).
		Merge(dagger.JSON(moduleTypesJSON), g.Config.ModuleConfig.ModuleName).
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
```

Where `genFuncs` is the `goTemplateFuncs` value already constructed for generation (the same one used to render templates). If it isn't in scope at that point, construct it (mirror how `generateCode`/`moduleMainSrc` build it) or expose `ModuleIntrospectionJSON` as a standalone func taking `(pkg, fset, moduleName)`.

- [ ] **Step 2:** Add imports as needed (`encoding/json`, `dagger.io/dagger`, `introspection`). `go build ./cmd/codegen/...` (clean).

- [ ] **Step 3:** `stg refresh`.

## Task 2.4: Privileged nesting + `--self-calls` argv in `baseWithCodegen`

**Files:**
- Modify: `core/sdk/go_sdk.go`

- [ ] **Step 1:** In `baseWithCodegen`, where `codegenArgs` is built, append `--self-calls` when the experimental feature is on:
```go
if sdkCfg := src.Self().SDK; sdkCfg != nil &&
	sdkCfg.ExperimentalFeatureEnabled(core.ModuleSourceExperimentalFeatureSelfCalls) {
	codegenArgs = append(codegenArgs, "--self-calls")
}
```

- [ ] **Step 2:** Add `experimentalPrivilegedNesting: true` to the codegen `withExec` selector's `Args` (so the in-container dag client works):
```go
{Name: "experimentalPrivilegedNesting", Value: dagql.NewBoolean(true)},
```

- [ ] **Step 3:** `go build ./core/... ./cmd/codegen/...` (clean). `go vet ./core/sdk/... ./cmd/codegen/...`.

- [ ] **Step 4:** Confirm the dagger Go client signatures used in Task 2.3 exist:
```bash
grep -n 'func (r \*Query) Schema\|func (r \*Schema) Merge\|func (r \*Schema) Contents' sdk/go/dagger.gen.go
```
If `Merge`/`Schema`/`Contents` differ from the plan's shapes, adjust Task 2.3's call accordingly (the regenerated client is the source of truth).

- [ ] **Step 5:** `stg refresh`.

## Task 2.5: Verify Patch 2 (idempotent no-op) + finalize

- [ ] **Step 1:**
```bash
go build ./core/... ./cmd/codegen/... && go vet ./core/sdk/... ./cmd/codegen/... && go test ./core/... ./cmd/codegen/...
```
Expected: clean. At this point the engine still self-appends, so the codegen-side merge short-circuits via `moduleAlreadyMerged` — no behavior change.

- [ ] **Step 2:** Finalize message:
```bash
stg edit -m "cmd/codegen + core/sdk: merge self-call types codegen-side via schemaTools

When --self-calls is set, generate-module emits the module's own
types as introspection JSON and merges them into the deps schema via
dag.schema(deps).merge(...). The merged schema drives binding
generation so user code can reference dag.MyModule() in a single
codegen pass.

baseWithCodegen passes experimentalPrivilegedNesting + --self-calls so
the in-container dag client works. Idempotent until the next patch:
the engine still appends self to deps, so the merge hits
moduleAlreadyMerged and is a no-op.

Signed-off-by: Yves Brissaud <yves@dagger.io>"
```

---

# PATCH 3 — stop engine-side self-append for Go

Goal: restore the `AsModuleTypes()` gate so the engine stops the `asModule` build+run for Go during codegen. Now the codegen-side merge is the real one and `generate-module` runs once.

## Task 3.1: Create patch + restore gate

**Files:**
- Modify: `core/schema/modulesource.go` (`runCodegen`, ~line 2419-2440)

- [ ] **Step 1:** `stg new go-sdk-drop-engine-self-append-go -m "WIP gate\n\nSigned-off-by: Yves Brissaud <yves@dagger.io>"`

- [ ] **Step 2:** Change the self-append condition from:
```go
if srcInst.Self().SDK != nil && isSelfCallsEnabled(srcInst) {
```
to:
```go
if _, ok := srcInst.Self().SDKImpl.AsModuleTypes(); ok && isSelfCallsEnabled(srcInst) {
```
Update the comment to explain: SDKs that implement `moduleTypes` (Python, TS) get the engine-side self-append; Go (no `moduleTypes`) merges self codegen-side, so the engine must not build+run it here.

- [ ] **Step 3:** `go build ./core/... && go vet ./core/schema/...` (clean). `stg refresh`.

## Task 3.2: Finalize

- [ ] **Step 1:**
```bash
go build ./core/... ./cmd/codegen/... && go test ./core/... ./cmd/codegen/...
```
Expected: clean.

- [ ] **Step 2:**
```bash
stg edit -m "core/schema/modulesource: stop engine-side self-append for Go

Restore the AsModuleTypes() gate on runCodegen's self-append. Go no
longer implements moduleTypes and now merges its own types into the
schema codegen-side (previous patch), so the engine must not build+run
the module via asModule just to discover them. This removes the
second generate-module + go build + binary exec for self-calls Go
modules: codegen now runs once.

Python/TS keep the engine-side self-append (they implement
moduleTypes).

Signed-off-by: Yves Brissaud <yves@dagger.io>"
```

---

# TASK 4 — End-to-end verification + parity check

This is the correctness gate for the emitter. Do it after Patch 3.

- [ ] **Step 1: Parity — emitter vs engine `asModule`.** Pick a representative self-calls Go module fixture under `core/integration/testdata/modules/go/` that exercises: an object with functions (args + return), an interface, an enum, a constructor, and a self-referential return. If none covers all, use the richest available (e.g. a self-calls fixture) and note gaps.

  Capture the merged schema two ways and diff:
  - **Engine path** (pre-Patch-3 behavior): on a scratch checkout with Patch 3 popped (`stg pop go-sdk-drop-engine-self-append-go`), regenerate the module and dump the introspection JSON the codegen received (add a temporary debug write of `cfg.IntrospectionJSON` post-merge, or capture via trace).
  - **Codegen path** (Patch 3 applied): regenerate and dump the merged JSON.

  Normalize (sort types/fields) and `diff`. They must be equivalent for the module's own types (`@sourceModuleName` matching the module). Investigate any difference in: optionality (`NON_NULL`), list nesting, enum values, arg defaults, directives (`@sourceMap`). Fixing divergence means correcting the emitter (Patch 1) — amend via `stg goto`/`refresh` only if the user approves touching it; otherwise add a follow-up patch.

- [ ] **Step 2: Single-pass confirmation.** Regenerate a self-calls Go module and confirm the trace shows `generate-module` once and **no** `asModule`/`go build`/binary-exec span for codegen-time discovery. (Use the engine-dev playground per the `engine-dev-testing` skill if needed.)

- [ ] **Step 3: Generated output correctness.** Confirm the regenerated `dagger.gen.go` contains the `dag.MyModule()` self binding and compiles; and that self types landed in `internal/dagger/<module>.gen.go` (file-splitter / `@sourceMap`), not the main file.

- [ ] **Step 4: Regressions.** Confirm a non-self-calls Go module still regenerates once and unchanged; confirm Python/TS modules are unaffected (engine-side self-append still fires for them — `AsModuleTypes()` true).

- [ ] **Step 5:** Report results to the user. If parity holds and single-pass is confirmed, the series is complete. `core/integration` tests are out of scope for these patches (separate follow-up), but flag any existing self-calls integration test that now exercises the new path.

---

## Out-of-scope (do NOT include)

- AST-based analyzer (`astscan`). `packages.Load` stays.
- Non-Go SDK changes.
- Changes to the runtime no-codegen path or its strict missing-files contract for `dagger call`.
- `core/integration` test additions (separate follow-up).
- Reorganizing/squashing the existing patch series (the user paused that; revisit after this lands).
