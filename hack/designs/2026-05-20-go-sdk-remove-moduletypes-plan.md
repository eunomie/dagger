# Go SDK remove moduleTypes — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the Go SDK's `moduleTypes` SDK call. Type discovery folds back into `generate-module` (the dispatcher template grows a `parentName == ""` arm). Self-calls keep working via the engine's existing `schemaTools`, with a small `Schema.MergeModule(modID)` extension so codegen hands the engine a Module ID. `packages.Load` stays — no AST switch.

**Architecture:** Three stg patches stacking on `go-sdk-runtime-skip-codegen`. (A) Template-only restoration of `TypeDefCode()` methods on parsed types + dispatcher wiring (~341 + ~25 lines, dead code from engine's perspective). (B) `Schema.MergeModule(modID)` engine extension + codegen-side wiring + `experimentalPrivilegedNesting` on the codegen exec (~60 lines, functionally a no-op until C lands because the merge is idempotent against the engine's existing self-append). (C) The flip: `AsModuleTypes` returns `nil, false`, delete the `ModuleTypes` body (~250-line deletion).

**Tech Stack:** Go, dagql, jen (for jen-emitted template code in `dave/jennifer/jen`), StGit. Reference commits in the experimentation branch `no-codegen-at-runtime-go`: `2ef3014e2` (template TypeDefCode restoration) and `272f82e80` (modules.go wiring — only the template-wiring portion of this commit is relevant; the AST switch and file deletions are explicitly OUT OF SCOPE).

**Reference:** spec at `hack/designs/2026-05-20-go-sdk-remove-moduletypes.md`.

---

## Prerequisites

- Working directory: `/home/yves/dev/src/github.com/dagger/dagger-worktrees/workspace-go-no-codegen-at-runtime`
- Branch: `workspace-go-no-codegen-at-runtime`
- Stack top must be `go-sdk-remove-moduletypes-design`. Verify with `stg series --all`.
- Each of the three patches is created empty with `stg new`, then `stg refresh`'d incrementally per task. Commit message finalized in the last task of each patch.
- Never `git commit` directly. Never `git push`.
- Never add `Co-Authored-By` to any commit message.
- Untracked `.claude/scheduled_tasks.lock` must not be staged.

Baseline green check before starting Patch A:

```bash
go build ./core/... ./cmd/codegen/... && go vet ./core/... ./cmd/codegen/... && go test ./core/sdk/... ./core/modules/... ./cmd/codegen/...
```

All must pass. If anything fails, stop and investigate before starting.

---

## File Structure

Per-patch file impact:

| Patch | Files touched | Net |
|---|---|---|
| A | `cmd/codegen/generator/go/templates/{module_funcs,module_objects,module_interfaces,module_enums,module_types,modules}.go` | +~365 lines, 0 deletions |
| B | `core/schematool.go`, `core/schema/schematool.go`, `cmd/codegen/generate_module.go`, `cmd/codegen/generator/go/generate_module.go`, `core/sdk/go_sdk.go` | +~60 lines |
| C | `core/sdk/go_sdk.go` | -~250 lines, +~10 lines |

No files created. No files deleted (per spec — `generate-typedefs` template stays around).

---

# PATCH A — empty-parentName-dispatch

## Task A.1: Create the empty stg patch for Patch A

**Files:** none — sets up the stg slot.

- [ ] **Step 1: Verify stack baseline**

```bash
stg series --all
```

Expected: top is `go-sdk-remove-moduletypes-design`, working tree only has `.claude/scheduled_tasks.lock` untracked.

- [ ] **Step 2: Create the empty patch**

```bash
stg new go-sdk-empty-parentname-dispatch -m "cmd/codegen/generator/go: restore TypeDefCode for empty-parentName dispatch

WIP — message finalized after the patch is complete.

Signed-off-by: Yves Brissaud <yves@dagger.io>"
```

- [ ] **Step 3: Confirm patch is empty top**

```bash
stg series --all
stg show --stat
```

Expected: `> go-sdk-empty-parentname-dispatch` at top with zero file changes.

---

## Task A.2: Add TypeDefCode methods to the five parsed-type files

**Files:**
- Modify: `cmd/codegen/generator/go/templates/module_funcs.go` (+133)
- Modify: `cmd/codegen/generator/go/templates/module_objects.go` (+70)
- Modify: `cmd/codegen/generator/go/templates/module_interfaces.go` (+28)
- Modify: `cmd/codegen/generator/go/templates/module_enums.go` (+49)
- Modify: `cmd/codegen/generator/go/templates/module_types.go` (+61)

The exact code is the verbatim content of the experimentation branch's commit `2ef3014e2`. The branch retained the same `packages.Load`-based `parseState` types we use here, so the diff applies as-is.

- [ ] **Step 1: Inspect the source commit so you know what you're transcribing**

```bash
git show 2ef3014e2 --stat
git show 2ef3014e2 -- cmd/codegen/generator/go/templates/module_funcs.go
```

Expected: 5 files changed, +341 insertions, 0 deletions. The diff is pure additions — `TypeDefCode()` methods sit alongside the existing `TypeDef()` methods on each parsed type.

- [ ] **Step 2: Apply the changes file by file**

For each of the five files, run:

```bash
git show 2ef3014e2 -- cmd/codegen/generator/go/templates/<file>.go
```

…and reproduce every `+` line at the indicated location in the workspace file (use `Edit` with the surrounding `type … struct` declaration as the anchor — the additions appear immediately after each existing struct definition). Do this in order: `module_funcs.go` first (it adds the `voidDef` helper that the other files reference), then `module_objects.go`, `module_interfaces.go`, `module_enums.go`, `module_types.go`.

`module_funcs.go` also requires adding the `dave/jennifer/jen` dot-import at the top of the imports block:

```go
. "github.com/dave/jennifer/jen" //nolint:staticcheck
```

(The other four files inherit the `jen` types via the shared package — no import change needed there.)

- [ ] **Step 3: Build & vet after each file**

After EACH file is edited:

```bash
go build ./cmd/codegen/...
```

If a file fails to build (most likely cause: `dotLine` or another helper not yet in scope), stop, re-read the source diff, fix the deviation, and rebuild. Go tolerates unused unexported functions at package scope, so `TypeDefCode()` methods without callers will not error — but they must compile.

- [ ] **Step 4: Final per-file vet**

```bash
go vet ./cmd/codegen/...
```

Expected: clean.

- [ ] **Step 5: Refresh the patch (not yet finalized)**

```bash
stg refresh
stg show --stat
```

Expected: 5 files changed, +341 insertions. No file outside `cmd/codegen/generator/go/templates/` should appear.

---

## Task A.3: Wire the new dispatcher arm in `modules.go`

**Files:**
- Modify: `cmd/codegen/generator/go/templates/modules.go` (the `moduleMainSrc` and `invokeSrc` functions)

This wires the `TypeDefCode()` methods from Task A.2 into the generated `invoke()` switch via a `createMod` jen `*Statement` threaded through the visitor pattern, plus a new `case ""` arm. The diff is a strict subset of the experimentation branch's commit `272f82e80` — only the template-wiring chunk, not the AST switch or file deletions.

- [ ] **Step 1: Inspect the reference diff (for the template wiring chunk only)**

```bash
git show 272f82e80 -- cmd/codegen/generator/go/templates/modules.go
```

Note: read only the `modules.go` chunk of that commit. The commit also deletes `generate_typedefs.go` and friends — **do not delete anything**; those are out of scope per the spec.

- [ ] **Step 2: In `moduleMainSrc`, initialize `createMod` before the visitor**

Locate `func (funcs goTemplateFuncs) moduleMainSrc() (string, error) {`, find the line `objFunctionCases := map[string][]Code{}`, and add immediately after it:

```go
	createMod := Qual("dag", "Module").Call()
```

(`Qual` is from the dot-imported `jen` package.)

- [ ] **Step 3: In `RootVisitor`, append `WithDescription` when there's a package doc**

Locate the `RootVisitor: func(pkgDoc string) error {` block. Currently it likely just `return nil`. Replace with:

```go
			RootVisitor: func(pkgDoc string) error {
				if pkgDoc != "" {
					createMod = dotLine(createMod, "WithDescription").Call(Lit(pkgDoc))
				}
				return nil
			},
```

- [ ] **Step 4: In `StructVisitor`, append `WithObject` after the existing implementation-code emission**

Locate the existing `StructVisitor` block (it ends with `implementationCode.Add(implCode).Line()` followed by `return nil`). Immediately before `return nil`, insert:

```go
				objTypeDefCode, err := objTypeSpec.TypeDefCode()
				if err != nil {
					return fmt.Errorf("failed to generate type def code for %s: %w", obj.Name(), err)
				}
				createMod = dotLine(createMod, "WithObject").Call(Add(Line(), objTypeDefCode))

```

- [ ] **Step 5: In `IfaceVisitor`, append `WithInterface` after the implementation-code emission**

Same pattern in the `IfaceVisitor` block:

```go
				ifaceTypeDefCode, err := ifaceTypeSpec.TypeDefCode()
				if err != nil {
					return fmt.Errorf("failed to generate type def code for %s: %w", obj.Name(), err)
				}
				createMod = dotLine(createMod, "WithInterface").Call(Add(Line(), ifaceTypeDefCode))

```

- [ ] **Step 6: In `EnumVisitor`, append `WithEnum` after the implementation-code emission**

```go
				enumTypeDefCode, err := enumTypeSpec.TypeDefCode()
				if err != nil {
					return fmt.Errorf("failed to generate type def code for %s: %w", obj.Name(), err)
				}
				createMod = dotLine(createMod, "WithEnum").Call(Add(Line(), enumTypeDefCode))

```

- [ ] **Step 7: Change `invokeSrc` signature to accept `createMod` and wire the empty-case arm**

Locate `invokeSrc(objFunctionCases)` (the call site within `moduleMainSrc`, currently at around line 193). Change to:

```go
		invokeSrc(objFunctionCases, createMod),
```

Then locate the function definition `func invokeSrc(objFunctionCases map[string][]Code) string {` (further down in the file). Change its signature:

```go
func invokeSrc(objFunctionCases map[string][]Code, createMod Code) string {
```

Inside `invokeSrc`, find the loop that appends per-object `Case` statements (`for _, objName := range objNames { … objCases = append(…) }`), and immediately after that loop but before the `Default()` append, insert:

```go
	// when the object name is empty, return the module definition
	objCases = append(objCases, Case(Lit("")).Block(
		Return(createMod, Nil()),
	))
```

- [ ] **Step 8: Build & vet**

```bash
go build ./cmd/codegen/... && go vet ./cmd/codegen/...
```

Expected: both clean.

- [ ] **Step 9: Run the codegen template tests**

```bash
go test ./cmd/codegen/...
```

Expected: pass. The new code path emits an additional `case ""` arm in any regenerated `dagger.gen.go`, but the existing tests don't pin the dispatcher's exact text.

- [ ] **Step 10: Refresh**

```bash
stg refresh
stg show --stat
```

Expected: cumulative ~366 lines added across the six template files.

---

## Task A.4: Smoke-verify generated output + finalize Patch A

**Files:** none changed.

- [ ] **Step 1: Manually regenerate one module's `dagger.gen.go` to confirm the new arm appears**

You don't need to commit any regenerated file. The point is to confirm the template is wired correctly. From the repo root:

```bash
# Pick any small Go SDK testdata module:
ls core/integration/testdata/modules/go/defaults/ | head
```

If you have an easy way to drive codegen for it (e.g. through the existing test framework or by hand), regenerate and grep:

```bash
grep -c 'case "":' <regenerated dagger.gen.go>
```

Expected: at least one match in the outer `invoke()` switch (alongside any existing inner-`fnName` `case ""` for constructors). If your environment doesn't make ad-hoc regen easy, skip this step — Patches B and C will surface any wiring bugs.

- [ ] **Step 2: Wider build/vet sweep**

```bash
go build ./... && go vet ./...
```

Pre-existing unrelated failure in `docs/current_docs/.../snippets/default-address/go/main.go` is acceptable (verified during the prior patch).

- [ ] **Step 3: Finalize Patch A's commit message**

```bash
stg edit -m "cmd/codegen/generator/go: restore TypeDefCode for empty-parentName dispatch

Restore the runtime typedef-registration branch that generate-module
emitted before the moduleTypes SDK method was split out. The
generated invoke() dispatcher now handles parentName == \"\" by
returning a *dagger.Module built via dag.Module().WithObject(...) at
runtime — the same shape the engine expects when it falls back to
the empty-function-name path.

Restores TypeDefCode() methods on the parsed object / interface /
enum / function / type-spec types so the generated bindings can
emit them at runtime, while keeping the newer TypeDef() runtime
helpers used by the existing generate-typedefs path.

Prereq for dropping AsModuleTypes on the Go SDK (Patch C in this
series): without this branch the runtime would return 'unknown
object' when the engine calls it via the empty-function-name
fallback.

This patch is dead code from the engine's perspective: moduleTypes
still routes Go modules through generate-typedefs, so the new
dispatcher arm is unreached until Patch C flips AsModuleTypes.

Signed-off-by: Yves Brissaud <yves@dagger.io>"
```

- [ ] **Step 4: Confirm**

```bash
stg series --all
stg show --stat
```

Expected stack:

```
+ … prior patches …
+ go-sdk-remove-moduletypes-design
> go-sdk-empty-parentname-dispatch    ← Patch A
```

---

# PATCH B — codegen-self-calls-merge

## Task B.1: Create the empty stg patch for Patch B

- [ ] **Step 1: Create empty patch on top of A**

```bash
stg new go-sdk-codegen-self-calls-merge -m "core/schematool + cmd/codegen: self-calls merge via Schema.MergeModule

WIP — message finalized after the patch is complete.

Signed-off-by: Yves Brissaud <yves@dagger.io>"
stg series --all
```

Expected: `> go-sdk-codegen-self-calls-merge` at the top.

---

## Task B.2: Add `Schema.MergeModule(modID)` to `core/schematool.go`

**Files:**
- Modify: `core/schematool.go` (add a method after the existing `Merge`)

- [ ] **Step 1: Inspect existing context**

Read `core/schematool.go` lines 105–141 to confirm the existing `Merge` signature:

```bash
sed -n '100,145p' core/schematool.go
```

Expected: `func (s *Schema) Merge(moduleTypes JSON, moduleName string) (*Schema, error) { … }` with documented idempotency via `moduleAlreadyMerged`.

- [ ] **Step 2: Insert `MergeModule` immediately after `Merge`**

Add the following after the `Merge` method (and before `isModuleDefinedType` which currently follows):

```go
// MergeModule merges a Module's types into the schema, returning the
// combined schema. Equivalent to Merge but takes a Module ID rather
// than introspection-shaped JSON — the engine derives the JSON from
// the module internally via the same SchemaBuilder.Append +
// SchemaIntrospectionJSONFileForModule path the engine uses for the
// self-append branch in core/schema/modulesource.go. This keeps the
// codegen binary free of any introspection-JSON conversion logic.
//
// Idempotent on the underlying Merge: re-merging the same module is
// a no-op (handled by moduleAlreadyMerged).
func (s *Schema) MergeModule(
	ctx context.Context,
	dag *dagql.Server,
	modID dagql.ID[*Module],
) (*Schema, error) {
	mod, err := modID.Load(ctx, dag)
	if err != nil {
		return nil, fmt.Errorf("load module for merge: %w", err)
	}
	root, err := CurrentQuery(ctx)
	if err != nil {
		return nil, fmt.Errorf("current query: %w", err)
	}
	// Build a one-shot SchemaBuilder containing just this module —
	// the engine's existing self-append pattern, scoped to a fresh
	// builder so only the module's own types end up in the JSON.
	builder := NewSchemaBuilder(root, nil).Append(NewUserMod(mod))
	jsonFile, err := builder.SchemaIntrospectionJSONFileForModule(ctx)
	if err != nil {
		return nil, fmt.Errorf("serialize module schema for merge: %w", err)
	}
	contents, err := jsonFile.Self().Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("read module schema JSON for merge: %w", err)
	}
	return s.Merge(JSON(contents), mod.Self().Name())
}
```

The imports already cover `context`, `fmt`, and (indirectly via the package) `dagql`. If `dagql.Server` or `dagql.ID` aren't yet imported, add `"github.com/dagger/dagger/dagql"` to the import block.

- [ ] **Step 3: Build**

```bash
go build ./core/...
```

If there's a compile error about `Module.Self().Name()`, check by reading `core/module.go` for the actual accessor — the Module struct has a `NameField string` and a likely `Name() string` method; adjust the call accordingly. If a compile error about `NewUserMod` or `NewSchemaBuilder` not being in scope, both live in the same `core` package as `Schema` (verified) so this should not happen.

- [ ] **Step 4: Vet & test**

```bash
go vet ./core/... && go test ./core/...
```

Expected: clean. `MergeModule` has no callers yet — Go allows it.

- [ ] **Step 5: Refresh**

```bash
stg refresh
stg show --stat
```

Expected: `core/schematool.go | ~35 +++++++` cumulative.

---

## Task B.3: Wire `mergeModule` into the dagql schema

**Files:**
- Modify: `core/schema/schematool.go` (extend the `*core.Schema` Fields block + add a resolver)

- [ ] **Step 1: Add the dagql field to `Install`**

Locate the `dagql.Fields[*core.Schema]{ … }.Install(srv)` block in `Install()` (around line 29). Add a new entry inside the braces, alongside the existing `merge` Func, **between** `merge` and the closing `}`:

```go
		dagql.Func("mergeModule", s.mergeModule).
			Doc(`Merge a Module's types into the schema. Equivalent to merge but takes a Module ID; the engine derives introspection JSON internally so SDKs don't need to convert it themselves.`).
			Args(
				dagql.Arg("module").Doc(`The module whose types to merge into this schema.`),
			),
```

- [ ] **Step 2: Add the resolver function**

After the existing `merge` resolver (around line 90–95), add:

```go
func (s *schemaToolsSchema) mergeModule(ctx context.Context, self *core.Schema, args struct {
	Module dagql.ID[*core.Module]
}) (*core.Schema, error) {
	dag, err := core.CurrentDagqlServer(ctx)
	if err != nil {
		return nil, fmt.Errorf("dagql server: %w", err)
	}
	return self.MergeModule(ctx, dag, args.Module)
}
```

- [ ] **Step 3: Build & vet**

```bash
go build ./core/... && go vet ./core/...
```

Expected: clean.

- [ ] **Step 4: Refresh**

```bash
stg refresh
stg show --stat
```

Expected: cumulative `core/schema/schematool.go | ~18 +++++++`.

---

## Task B.4: Flip `getGlobalConfig` for `generate-module`

**Files:**
- Modify: `cmd/codegen/generate_module.go:35`

- [ ] **Step 1: Apply the one-line change**

Locate the line:

```go
	cfg, err := getGlobalConfig(ctx, false)
```

Change `false` to `true`:

```go
	cfg, err := getGlobalConfig(ctx, true)
```

This forces the codegen container to always open a dag client (required for the upcoming `MergeModule` dagql call). Sibling subcommand `generate-typedef.go:26` already uses `true`.

- [ ] **Step 2: Build**

```bash
go build ./cmd/codegen/...
```

Expected: clean.

- [ ] **Step 3: Refresh**

```bash
stg refresh
stg show --stat
```

Expected: cumulative now also lists `cmd/codegen/generate_module.go | 2 +-`.

---

## Task B.5: Add `--self-calls` flag wiring + merge logic in `cmd/codegen/generator/go/generate_module.go`

**Files:**
- Modify: `cmd/codegen/generator/go/generate_module.go`

- [ ] **Step 1: Read the current `GenerateModule` flow**

```bash
sed -n '1,80p' cmd/codegen/generator/go/generate_module.go
```

Identify where the `*GoGenerator`'s `GenerateModule` reads its config and produces the generated state.

- [ ] **Step 2: Add a `SelfCalls` field on `ModuleGeneratorConfig`**

Locate `type ModuleGeneratorConfig struct` (in `cmd/codegen/generator/generator.go` or a similar shared file — find it via `grep -rn "type ModuleGeneratorConfig" cmd/codegen/generator/`). Add:

```go
	// SelfCalls indicates the module has the self-calls experimental feature
	// enabled. When true, generate-module merges the module's own types into
	// the introspection schema (via Schema.MergeModule) before generating
	// bindings, so user code can reference its own module's types.
	SelfCalls bool
```

- [ ] **Step 3: Wire the CLI flag in `cmd/codegen/generate_module.go`**

In the file's `init()` function (alongside the other `Flags().StringVar(...)` registrations for `generateModuleCmd`), add:

```go
	generateModuleCmd.Flags().BoolVar(&selfCalls, "self-calls", false, "merge the module's own types into the schema for self-call bindings")
```

And declare the var at the top of the file alongside other package-level vars (`var selfCalls bool`).

Then in `GenerateModule` itself, set `moduleConfig.SelfCalls = selfCalls` before passing the config to the generator (mirroring the other field assignments).

- [ ] **Step 4: In `cmd/codegen/generator/go/generate_module.go`, add the merge step**

After `loadPackage` succeeds and BEFORE the binding generation begins (search for `loadPackage` in the file to find the location), add:

```go
	if g.Config.ModuleConfig != nil && g.Config.ModuleConfig.SelfCalls && g.Config.Dag != nil {
		mergedJSON, err := mergeSelfTypesIntoSchema(ctx, g.Config, pkg)
		if err != nil {
			return nil, fmt.Errorf("merge self-call types into schema: %w", err)
		}
		// Re-set the schema so subsequent generation uses the merged version.
		var resp introspection.Response
		if err := json.Unmarshal([]byte(mergedJSON), &resp); err != nil {
			return nil, fmt.Errorf("unmarshal merged introspection JSON: %w", err)
		}
		schema = resp.Schema
		generator.SetSchema(schema)
	}
```

(`json`, `introspection`, `generator` should already be in the file's imports; add any that aren't.)

- [ ] **Step 5: Add the `mergeSelfTypesIntoSchema` helper at the bottom of the same file**

```go
// mergeSelfTypesIntoSchema builds a dagger.Module from the parsed
// module package's types, then asks the engine to merge it into the
// introspection schema via Schema.MergeModule. Returns the merged
// introspection JSON.
func mergeSelfTypesIntoSchema(
	ctx context.Context,
	cfg generator.Config,
	pkg *packages.Package,
) (string, error) {
	dag := cfg.Dag

	// Reuse the existing TypeDefs() template helper to build the
	// dag.Module().WithObject(...).WithInterface(...).WithEnum(...)
	// chain from the parsed package — same shape Patch A's
	// TypeDefCode emits at runtime, but constructed via the live dag
	// client at codegen time. Returns a JSON-encoded dagger.ModuleID.
	gen := templates.GoTypeDefsGenerator(ctx, generator.GetSchema(), cfg.SchemaVersion(), cfg, pkg, /* fset */ nil, 0)
	modJSONID, err := gen.TypeDefs()
	if err != nil {
		return "", fmt.Errorf("extract module typedefs for self-call merge: %w", err)
	}

	var modID dagger.ModuleID
	if err := json.Unmarshal([]byte(modJSONID), &modID); err != nil {
		return "", fmt.Errorf("decode module ID for self-call merge: %w", err)
	}
	mergedJSON, err := dag.Schema(cfg.IntrospectionJSON).
		MergeModule(dag.LoadModuleFromID(modID)).
		Contents(ctx)
	if err != nil {
		return "", fmt.Errorf("dagql merge module into schema: %w", err)
	}
	return mergedJSON, nil
}
```

> **Implementation note:** the exact dagger Go-SDK client API (`dag.Schema(...).MergeModule(modID).Contents(ctx)`) depends on what `dagger.gen.go` regenerates after Patch B lands on the engine schema. The shape above is the conceptual contract. If you find the regenerated client surface differs (e.g. requires `*ModuleID` instead of `ModuleID`, or `Contents()` returns `string` instead of `(string, error)`), adjust to match the actual generated signature — the JSON output is the source of truth. Use `grep -rn "func (r \*Schema)" sdk/go/dagger/` or `internal/dagger/dagger.gen.go` (after `go generate`) to confirm.

- [ ] **Step 6: Verify the `templates.GoTypeDefsGenerator` API still exists**

```bash
grep -n "GoTypeDefsGenerator" cmd/codegen/generator/go/templates/typedefs.go
```

Expected: the function still exists. The spec explicitly keeps `generate-typedefs` code around; the helper is reused at codegen time inside `mergeSelfTypesIntoSchema`.

- [ ] **Step 7: Build & vet**

```bash
go build ./cmd/codegen/... && go vet ./cmd/codegen/...
```

Expected: clean. If there are import issues for `dagger` (the Go SDK client), add `"dagger.io/dagger"` to the import block.

- [ ] **Step 8: Refresh**

```bash
stg refresh
stg show --stat
```

Expected: incremental additions across `generator.go`, `generate_module.go` (root), `generator/go/generate_module.go`.

---

## Task B.6: Add `experimentalPrivilegedNesting` + `--self-calls` to `baseWithCodegen`

**Files:**
- Modify: `core/sdk/go_sdk.go` (the codegen `withExec` selector inside `baseWithCodegen`)

- [ ] **Step 1: Locate the codegen withExec**

```bash
grep -n 'codegen.*generate-module\|"codegen"\s*$\|codegenArgs' core/sdk/go_sdk.go | head
```

The `withExec` selector in `baseWithCodegen` runs `codegen generate-module …`. Today it does NOT pass `experimentalPrivilegedNesting`.

- [ ] **Step 2: Add `experimentalPrivilegedNesting: true` to the codegen withExec selector args**

Find the selector that looks like:

```go
		dagql.Selector{
			Field: "withExec",
			Args: []dagql.NamedInput{
				{
					Name: "args",
					Value: append(dagql.ArrayInput[dagql.String]{
						"codegen",
					}, codegenArgs...),
				},
			},
		},
```

Add a sibling `NamedInput` for privileged nesting:

```go
		dagql.Selector{
			Field: "withExec",
			Args: []dagql.NamedInput{
				{
					Name: "args",
					Value: append(dagql.ArrayInput[dagql.String]{
						"codegen",
					}, codegenArgs...),
				},
				{
					Name:  "experimentalPrivilegedNesting",
					Value: dagql.Boolean(true),
				},
			},
		},
```

- [ ] **Step 3: Conditionally pass `--self-calls` to the codegen argv**

Locate where `codegenArgs` is built (a `dagql.ArrayInput[dagql.String]` literal earlier in the function). Immediately after it, add:

```go
	if sdkCfg := src.Self().SDK; sdkCfg != nil &&
		sdkCfg.ExperimentalFeatureEnabled(core.ModuleSourceExperimentalFeatureSelfCalls) {
		codegenArgs = append(codegenArgs, "--self-calls")
	}
```

This mirrors the existing branch's pattern in `baseWithCodegen`. `ExperimentalFeatureEnabled` is defined at `core/modulesource.go:150`; the feature constant is at `core/modulesource.go:2178`.

- [ ] **Step 4: Build & vet**

```bash
go build ./core/sdk/... && go vet ./core/sdk/...
```

Expected: clean.

- [ ] **Step 5: Refresh**

```bash
stg refresh
stg show --stat
```

Expected: `core/sdk/go_sdk.go | ~10 +++` cumulative.

---

## Task B.7: Verify Patch B end-to-end + finalize commit message

**Files:** none changed.

- [ ] **Step 1: Wider build/vet/test**

```bash
go build ./core/... ./cmd/codegen/...
go vet ./core/... ./cmd/codegen/...
go test ./core/... ./cmd/codegen/...
```

Expected: all clean (the `docs/current_docs/.../snippets` pre-existing failure is unrelated — verified in Patch A's verification).

- [ ] **Step 2: Manual smoke — codegen still works on a non-self-calls module**

If you have a small Go SDK testdata module handy, exercise the codegen path against it. Expected: codegen succeeds. The new `MergeModule` code path is only entered when `SelfCalls` is true, so a non-self-calls module exercises the new privileged-nesting wiring (dag connection works) without invoking `MergeModule` itself.

- [ ] **Step 3: Finalize Patch B's commit message**

```bash
stg edit -m "core/schematool + cmd/codegen: self-calls merge via Schema.MergeModule

Add Schema.MergeModule(modID) to engine-side schemaTools and wire
generate-module to call it when --self-calls is set. The codegen
binary builds a dag.Module().WithObject(...) shape from its
packages.Load output (same shape Patch A's TypeDefCode emits at
runtime), gets a Module ID, and asks the engine to merge it into the
introspection schema. The merged schema then drives binding
generation so user code can reference its own module's types.

Schema.MergeModule reuses the engine's existing SchemaBuilder.Append
+ SchemaIntrospectionJSONFileForModule pipeline — the same code the
engine's self-append branch already runs — to derive introspection
JSON for just the module. This keeps the codegen binary free of any
introspection-JSON conversion code.

baseWithCodegen now passes experimentalPrivilegedNesting: true on the
codegen withExec so the in-container dag client works. This is a
one-time buildkit cache key change for all Go SDK modules; nothing
else moves until Patch C in this series.

Net change in privileged execs at this point: zero — moduleTypes
already runs privileged today, and Patch C deletes that path. We
just hold both for one patch.

This patch is functionally a no-op until Patch C lands: the engine
still appends self to deps before calling Codegen, so the codegen-
side MergeModule call short-circuits via Schema.Merge's
moduleAlreadyMerged check. That idempotency is what makes the
patch safe to ship alone.

Signed-off-by: Yves Brissaud <yves@dagger.io>"
```

- [ ] **Step 4: Confirm stack**

```bash
stg series --all
stg show --stat
```

Expected:

```
+ … prior patches …
+ go-sdk-empty-parentname-dispatch
> go-sdk-codegen-self-calls-merge
```

with diff touching `core/schematool.go`, `core/schema/schematool.go`, `cmd/codegen/generate_module.go`, `cmd/codegen/generator/generator.go`, `cmd/codegen/generator/go/generate_module.go`, `core/sdk/go_sdk.go`.

---

# PATCH C — drop-go-sdk-moduletypes

## Task C.1: Create the empty stg patch for Patch C

- [ ] **Step 1: Create empty patch on top of B**

```bash
stg new go-sdk-drop-moduletypes -m "core/sdk/go_sdk: drop moduleTypes implementation

WIP — message finalized after the patch is complete.

Signed-off-by: Yves Brissaud <yves@dagger.io>"
stg series --all
```

Expected: `> go-sdk-drop-moduletypes` at top.

---

## Task C.2: Flip `AsModuleTypes()` to `(nil, false)` + delete `ModuleTypes` body

**Files:**
- Modify: `core/sdk/go_sdk.go`

- [ ] **Step 1: Locate the two functions**

```bash
grep -n "func (sdk \*goSDK) AsModuleTypes\|func (sdk \*goSDK) ModuleTypes" core/sdk/go_sdk.go
```

Expected: `AsModuleTypes` around line 57; `ModuleTypes` body starting around line 250 and ending around line 449.

- [ ] **Step 2: Replace `AsModuleTypes` body**

Find:

```go
func (sdk *goSDK) AsModuleTypes() (core.ModuleTypes, bool) {
	return sdk, true
}
```

Replace with:

```go
func (sdk *goSDK) AsModuleTypes() (core.ModuleTypes, bool) {
	// Go SDK no longer implements moduleTypes; the runtime container's
	// empty-parentName dispatcher (generated by Patch A in this series)
	// handles type discovery. Engine falls through to the Runtime +
	// empty-function-name path in runModuleDefInSDK automatically.
	return nil, false
}
```

- [ ] **Step 3: Delete the entire `ModuleTypes` method**

Find:

```go
func (sdk *goSDK) ModuleTypes(
	ctx context.Context,
	deps *core.SchemaBuilder,
	src dagql.ObjectResult[*core.ModuleSource],
	partiallyInitializedMod *core.Module,
) (inst dagql.ObjectResult[*core.Module], rerr error) {
```

Delete the entire function — opening `func` line through the matching closing `}`. This removes ~250 lines.

- [ ] **Step 4: Build**

```bash
go build ./core/sdk/...
```

Expected: clean. If there are unused-import errors, proceed to Task C.3. If there are "undefined identifier" errors referencing things outside `ModuleTypes`, stop and investigate — we should not have any external callers of `ModuleTypes` (it's only called via the interface).

- [ ] **Step 5: Refresh**

```bash
stg refresh
stg show --stat
```

Expected: `core/sdk/go_sdk.go | ~250 ---------------`.

---

## Task C.3: Clean up imports and constants no longer referenced

**Files:**
- Modify: `core/sdk/go_sdk.go` (imports block + constants)

- [ ] **Step 1: Run vet to find unused symbols**

```bash
go vet ./core/sdk/...
```

Vet may report unused imports. Common candidates after deleting `ModuleTypes`:

- `encoding/json` (only used to unmarshal the moduleTypes ID return JSON)
- `engineutil` (used for `ExecutionMetadata`/`CallDigest`)

- [ ] **Step 2: Check unused constants**

```bash
grep -n "GoSDKModuleIDPath\|goSDKExecMDDigest\|goSDKIntrospectionJSONPath" core/sdk/go_sdk.go
```

`GoSDKModuleIDPath` and `goSDKExecMDDigest` were referenced ONLY by `ModuleTypes`. If they appear ONLY in their declaration lines now (no other call sites), delete the declarations. `goSDKIntrospectionJSONPath` is still used by `baseWithCodegen` — leave it.

- [ ] **Step 3: Remove the unused imports / constants identified above**

Apply targeted `Edit` calls. For example, remove `"encoding/json"` from the import block; remove the `GoSDKModuleIDPath` line from the const block; remove the `var goSDKExecMDDigest = …` line.

- [ ] **Step 4: Rebuild & vet**

```bash
go build ./core/sdk/... && go vet ./core/sdk/...
```

Expected: both clean.

- [ ] **Step 5: Run scoped tests**

```bash
go test ./core/sdk/... ./core/modules/... ./core/...
```

Expected: pass. The engine code paths gated on `AsModuleTypes()` (lines ~2423, ~2825, ~3097 in `core/schema/modulesource.go`) will, for Go modules, now take the alternate branches. Existing unit tests under `core/...` don't exercise the full module-load round-trip, so they continue to pass; the integration story is deferred per the spec.

- [ ] **Step 6: Refresh**

```bash
stg refresh
stg show --stat
```

Expected: cumulative `core/sdk/go_sdk.go | ~260 --` (deletions slightly bigger than +10 of additions).

---

## Task C.4: Wider verification + finalize commit message

**Files:** none changed.

- [ ] **Step 1: Wider build/vet sweep**

```bash
go build ./core/... ./cmd/codegen/...
go vet ./core/... ./cmd/codegen/...
```

Expected: clean.

- [ ] **Step 2: Run the broader test suite for the changed packages**

```bash
go test ./core/sdk/... ./core/modules/... ./cmd/codegen/...
```

Expected: clean.

- [ ] **Step 3: Inspect file shape**

```bash
grep -n "func (sdk \*goSDK)" core/sdk/go_sdk.go
```

Expected: `ModuleTypes` is gone; remaining methods are `AsRuntime`, `AsModuleTypes` (now stub), `AsCodeGenerator`, `AsClientGenerator`, `RequiredClientGenerationFiles`, `GenerateClient`, `Codegen`, `Runtime`, `baseWithCodegen`, `baseWithoutCodegen`, `base`, `getUnixSocketSelector`.

- [ ] **Step 4: Finalize Patch C's commit message**

```bash
stg edit -m "core/sdk/go_sdk: drop moduleTypes implementation

Go SDK now handles type discovery entirely through generate-module
(Patch A's empty-parentName dispatcher arm) plus optional self-calls
merge at codegen time (Patch B's Schema.MergeModule). AsModuleTypes
returns nil, false so the engine falls through to the Runtime +
empty-function-name path at asModule time — the same path that
existed before moduleTypes was introduced in PR #10584.

Effects on the engine paths in core/schema/modulesource.go:

  - runModuleDefInSDK: skips the moduleTypes branch, uses the
    runtime container's invoke() dispatch on empty parent name.
  - runGeneratedCodeDirectory + initializeSDKModule: skip the
    mod.Deps = mod.Deps.Append(mod) self-append. Codegen receives
    deps-without-self, and Patch B's MergeModule call becomes the
    sole source of merged JSON for self-calls modules.

Drops the now-unused ModuleTypes method, the goSDKExecMDDigest
constant, the GoSDKModuleIDPath constant, and the imports those
required.

Other SDKs (Python, TypeScript, …) continue to implement
AsModuleTypes; the engine-side interface and dispatch stay
untouched. core/sdk.go's ModuleTypes interface is unchanged.

The generate-typedefs CLI subcommand and template stay around —
they are no longer reached for Go modules but the code is left in
place for a possible follow-up cleanup patch.

Signed-off-by: Yves Brissaud <yves@dagger.io>"
```

- [ ] **Step 5: Final stack check**

```bash
stg series --all
stg show --stat
```

Expected final stack:

```
+ legacy-codegen-flag
+ go-sdk-skip-codegen-runtime-design
+ go-sdk-skip-codegen-runtime-plan
+ go-sdk-runtime-skip-codegen
+ go-sdk-remove-moduletypes-design
+ go-sdk-empty-parentname-dispatch
+ go-sdk-codegen-self-calls-merge
> go-sdk-drop-moduletypes
! schematool-library
```

---

## Out-of-scope follow-ups (do NOT include in this series)

- Integration tests under `core/integration` covering the new self-calls path and the empty-parentName fallback.
- Deleting `generate-typedefs` subcommand + template (trivial follow-up once integration coverage proves the new path is sound).
- Removing the `core/sdk.go` `ModuleTypes` interface (only safe after every SDK migrates).
- Migrating other SDKs (Python, TypeScript, …) to the same pattern.
- AST-based source analysis (explicit non-goal of this series).

Each of these belongs to its own future patch series.
