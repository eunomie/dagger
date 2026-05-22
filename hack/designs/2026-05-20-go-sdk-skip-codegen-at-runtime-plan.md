# Go SDK skip codegen at runtime — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a Go module's `dagger.json` has `codegen.legacyCodegenAtRuntime: false`, the Go SDK's `Runtime()` skips `codegen generate-module` and builds straight from the committed `dagger.gen.go` + `internal/dagger/dagger.gen.go`.

**Architecture:** Add three small additions to `core/sdk/go_sdk.go`: a `useRuntimeCodegen` predicate, a `requireGeneratedFiles` precheck, and a sibling `baseWithoutCodegen` helper next to the existing `baseWithCodegen`. `Runtime()` picks between the two helpers; `Codegen()` and `ModuleTypes()` are untouched. The new helper returns an extra `[]dagql.Selector` (the SSH-unset pair) so the caller can append it after the `go build` exec.

**Tech Stack:** Go, dagql, StGit. All edits scoped to a single file.

**Reference:** spec at `hack/designs/2026-05-20-go-sdk-skip-codegen-at-runtime.md`.

---

## Prerequisites

- Working directory: `/home/yves/dev/src/github.com/dagger/dagger-worktrees/workspace-go-no-codegen-at-runtime`
- Branch: `workspace-go-no-codegen-at-runtime`
- Stack top must be the design patch `go-sdk-skip-codegen-runtime-design` (which stacks on `legacy-codegen-flag`). Verify with `stg series --all`.
- Every change goes into one stg patch. We create the patch empty up front, then `stg refresh` after each task.
- Commit message gets finalized in Task 6.
- Never `git commit` directly. Never `git push`.

Baseline green check:

```bash
go build ./core/sdk/... && go vet ./core/sdk/...
```

If either fails, stop and investigate before starting.

---

## File Structure

Only one file is modified:

| File | Responsibility |
|---|---|
| `core/sdk/go_sdk.go` | All three new functions + the `Runtime()` branch. Sibling-of-`baseWithCodegen` placement keeps the new and legacy paths next to each other and easy to compare. |

No new files. No moved files. No test files in this patch (integration coverage is the next patch per the spec's Scope).

---

## Task 1: Create the empty stg patch

**Files:** none yet — sets up the stg slot.

- [ ] **Step 1: Verify stack baseline**

```bash
stg series --all
```

Expected: top patch is `go-sdk-skip-codegen-runtime-design`, no dirty working tree (other than `.claude/scheduled_tasks.lock`, which we never stage).

- [ ] **Step 2: Create the patch**

```bash
stg new go-sdk-runtime-skip-codegen -m "core/sdk/go_sdk: skip codegen at runtime when opted in

WIP — message finalized in the last task.

Signed-off-by: Yves Brissaud <yves@dagger.io>"
```

- [ ] **Step 3: Verify the patch is the new top, empty**

```bash
stg series --all
stg show --stat
```

Expected: `> go-sdk-runtime-skip-codegen` at the top with zero file changes.

---

## Task 2: Add `useRuntimeCodegen` predicate

**Files:**
- Modify: `core/sdk/go_sdk.go` (add the function immediately above `baseWithCodegen`, around current line 544)

- [ ] **Step 1: Open the file at the right spot**

Locate `func (sdk *goSDK) baseWithCodegen(` (current line 544). The new function goes immediately above it, between the closing `}` of `Runtime()` and the opening of `baseWithCodegen`.

- [ ] **Step 2: Insert the predicate**

Paste this exactly above `func (sdk *goSDK) baseWithCodegen(`:

```go
// useRuntimeCodegen reports whether the SDK should run codegen during
// runtime operations (dagger call, dagger functions). True unless the
// module has explicitly set codegen.legacyCodegenAtRuntime=false in
// dagger.json. Unset / nil defaults to true to preserve legacy behavior.
func useRuntimeCodegen(src dagql.ObjectResult[*core.ModuleSource]) bool {
	cfg := src.Self().CodegenConfig
	if cfg == nil || cfg.LegacyCodegenAtRuntime == nil {
		return true
	}
	return *cfg.LegacyCodegenAtRuntime
}

```

- [ ] **Step 3: Build & vet**

```bash
go build ./core/sdk/... && go vet ./core/sdk/...
```

Expected: both succeed. (Go allows unused unexported functions at package scope, so adding without a caller is fine.)

- [ ] **Step 4: Refresh the patch**

```bash
stg refresh
stg show --stat
```

Expected: `core/sdk/go_sdk.go | 13 ++++` (or similar — one block added).

---

## Task 3: Add `requireGeneratedFiles` helper

**Files:**
- Modify: `core/sdk/go_sdk.go` (place immediately below `useRuntimeCodegen`, still above `baseWithCodegen`)

- [ ] **Step 1: Insert the helper**

Below the `useRuntimeCodegen` block and above `baseWithCodegen`, paste exactly:

```go
// requireGeneratedFiles verifies the module's committed generated files
// are present. Called only on the no-codegen path; the legacy path
// regenerates them so the check would be redundant.
//
// The error template intentionally uses "(re)generate" so the same
// message reads naturally for both first-time opt-in and stale
// post-modification cases.
func requireGeneratedFiles(
	ctx context.Context,
	dag *dagql.Server,
	contextDir dagql.ObjectResult[*core.Directory],
	srcSubpath, modName string,
) error {
	required := []string{
		filepath.Join(srcSubpath, "dagger.gen.go"),
		filepath.Join(srcSubpath, "internal", "dagger", "dagger.gen.go"),
	}
	for _, rel := range required {
		var exists dagql.Boolean
		err := dag.Select(ctx, contextDir, &exists,
			dagql.Selector{
				Field: "exists",
				Args:  []dagql.NamedInput{{Name: "path", Value: dagql.NewString(rel)}},
			},
		)
		if err != nil {
			return fmt.Errorf("check generated file %q: %w", rel, err)
		}
		if !bool(exists) {
			return fmt.Errorf(
				"module %q has codegen.legacyCodegenAtRuntime=false but generated file %q is missing; "+
					"run `dagger develop` to (re)generate",
				modName, rel)
		}
	}
	return nil
}

```

- [ ] **Step 2: Build & vet**

```bash
go build ./core/sdk/... && go vet ./core/sdk/...
```

Expected: both succeed. The function references `context`, `fmt`, `filepath`, `dagql`, `core` — all already imported by `go_sdk.go`.

- [ ] **Step 3: Refresh**

```bash
stg refresh
stg show --stat
```

Expected: `core/sdk/go_sdk.go | ~45 +++++` cumulative since Task 2.

---

## Task 4: Add `baseWithoutCodegen` helper

**Files:**
- Modify: `core/sdk/go_sdk.go` (place immediately below `requireGeneratedFiles`, still above `baseWithCodegen`)

- [ ] **Step 1: Insert the helper**

Paste exactly below `requireGeneratedFiles` and above `func (sdk *goSDK) baseWithCodegen(`:

```go
// baseWithoutCodegen prepares the runtime container when the module
// has opted out of runtime codegen (codegen.legacyCodegenAtRuntime=false).
// It mounts the module context directory as-is (no withoutFile, no
// schema JSON, no codegen exec), verifies the expected generated files
// are present, and applies the same gitconfig + SSH selectors as the
// legacy path so `go build` can fetch private modules.
//
// Returns the container plus a list of selectors the caller must
// append after the `go build` exec — currently the SSH socket unset
// pair, which has to bracket the build because `go build` is the
// first download trigger in this path.
func (sdk *goSDK) baseWithoutCodegen(
	ctx context.Context,
	src dagql.ObjectResult[*core.ModuleSource],
) (dagql.ObjectResult[*core.Container], []dagql.Selector, error) {
	var ctr dagql.ObjectResult[*core.Container]

	dag, err := sdk.root.Server.Server(ctx)
	if err != nil {
		return ctr, nil, fmt.Errorf("failed to get dag for go module sdk runtime: %w", err)
	}

	modName := src.Self().ModuleOriginalName
	contextDir := src.Self().ContextDirectory
	srcSubpath := src.Self().SourceSubpath

	if err := requireGeneratedFiles(ctx, dag, contextDir, srcSubpath, modName); err != nil {
		return ctr, nil, err
	}

	ctr, err = sdk.base(ctx)
	if err != nil {
		return ctr, nil, err
	}

	contextDirID, err := contextDir.ID()
	if err != nil {
		return ctr, nil, fmt.Errorf("failed to get module context directory ID: %w", err)
	}

	selectors := []dagql.Selector{
		{
			Field: "withMountedDirectory",
			Args: []dagql.NamedInput{
				{Name: "path", Value: dagql.NewString(goSDKUserModContextDirPath)},
				{Name: "source", Value: dagql.NewID[*core.Directory](contextDirID)},
			},
		},
		{
			Field: "withWorkdir",
			Args: []dagql.NamedInput{
				{Name: "path", Value: dagql.NewString(
					filepath.Join(goSDKUserModContextDirPath, srcSubpath))},
			},
		},
	}

	// goSDKConfig parity (GOPRIVATE etc.) — same decoding the legacy
	// path does in baseWithCodegen.
	var cfg goSDKConfig
	var meta mapstructure.Metadata
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Metadata: &meta,
		Result:   &cfg,
	})
	if err != nil {
		return ctr, nil, err
	}
	if err := decoder.Decode(sdk.rawConfig); err != nil {
		return ctr, nil, err
	}
	if len(meta.Unused) > 0 {
		return ctr, nil, fmt.Errorf("unknown sdk config keys found %v", meta.Unused)
	}
	selectors = append(selectors, getSDKConfigSelectors(ctx, cfg)...)

	// gitconfig + SSH parity so `go build` can fetch private modules.
	bk, err := sdk.root.Engine(ctx)
	if err != nil {
		return ctr, nil, err
	}
	gitSelectors, err := gitConfigSelectors(ctx, bk)
	if err != nil {
		return ctr, nil, err
	}
	selectors = append(selectors, gitSelectors...)

	setSSH, unsetSSH, err := sdk.getUnixSocketSelector(ctx)
	if err != nil {
		return ctr, nil, err
	}
	selectors = append(selectors, setSSH...)

	if err := dag.Select(ctx, ctr, &ctr, selectors...); err != nil {
		return ctr, nil, fmt.Errorf("failed to prepare go module runtime container: %w", err)
	}
	return ctr, unsetSSH, nil
}

```

- [ ] **Step 2: Build & vet**

```bash
go build ./core/sdk/... && go vet ./core/sdk/...
```

Expected: both succeed. All referenced helpers (`sdk.base`, `sdk.root.Server.Server`, `sdk.root.Engine`, `getSDKConfigSelectors`, `gitConfigSelectors`, `sdk.getUnixSocketSelector`) and all constants (`goSDKUserModContextDirPath`) already exist in `go_sdk.go`. `mapstructure` is already imported.

- [ ] **Step 3: Refresh**

```bash
stg refresh
stg show --stat
```

Expected: cumulative `core/sdk/go_sdk.go` diff ~135 lines added.

---

## Task 5: Branch `Runtime()` to use the helpers

**Files:**
- Modify: `core/sdk/go_sdk.go:472-533` (the body of `Runtime()` from the `baseWithCodegen` call through the closing of the existing `dag.Select` block)

- [ ] **Step 1: Replace the `baseWithCodegen` call site**

Current code, lines 472-475:

```go
	ctr, err := sdk.baseWithCodegen(ctx, deps, source)
	if err != nil {
		return nil, err
	}
```

Replace with:

```go
	var ctr dagql.ObjectResult[*core.Container]
	var postBuildSelectors []dagql.Selector
	if useRuntimeCodegen(source) {
		ctr, err = sdk.baseWithCodegen(ctx, deps, source)
	} else {
		ctr, postBuildSelectors, err = sdk.baseWithoutCodegen(ctx, source)
	}
	if err != nil {
		return nil, err
	}
```

- [ ] **Step 2: Replace the inline `dag.Select(...)` block to thread `postBuildSelectors`**

Current code, lines 476-533 (the block starting `if err := dag.Select(ctx, ctr, &ctr,` and ending with the closing `); err != nil { ... }`):

```go
	if err := dag.Select(ctx, ctr, &ctr,
		dagql.Selector{
			Field: "withExec",
			Args: []dagql.NamedInput{
				{
					Name: "args",
					Value: dagql.ArrayInput[dagql.String]{
						"go", "build",
						"-ldflags", "-s -w", // strip DWARF debug symbols to save a few MBs of space
						"-o", goSDKRuntimePath,
						".",
					},
				},
			},
		},
		dagql.Selector{
			Field: "withEntrypoint",
			Args: []dagql.NamedInput{
				{
					Name: "args",
					Value: dagql.ArrayInput[dagql.String]{
						goSDKRuntimePath,
					},
				},
			},
		},
		dagql.Selector{
			Field: "withWorkdir",
			Args: []dagql.NamedInput{
				{
					Name:  "path",
					Value: dagql.NewString(RuntimeWorkdirPath),
				},
			},
		},
		// remove shared cache mounts from final container so module code can't
		// do weird things with them like IPC, etc.
		dagql.Selector{
			Field: "withoutMount",
			Args: []dagql.NamedInput{
				{
					Name:  "path",
					Value: dagql.String("/go/pkg/mod"),
				},
			},
		},
		dagql.Selector{
			Field: "withoutMount",
			Args: []dagql.NamedInput{
				{
					Name:  "path",
					Value: dagql.String("/root/.cache/go-build"),
				},
			},
		},
	); err != nil {
		return nil, fmt.Errorf("failed to build go runtime binary: %w", err)
	}
```

Replace with:

```go
	runtimeSelectors := []dagql.Selector{
		{
			Field: "withExec",
			Args: []dagql.NamedInput{
				{
					Name: "args",
					Value: dagql.ArrayInput[dagql.String]{
						"go", "build",
						"-ldflags", "-s -w", // strip DWARF debug symbols to save a few MBs of space
						"-o", goSDKRuntimePath,
						".",
					},
				},
			},
		},
	}
	// postBuildSelectors is the SSH-unset pair when the no-codegen path
	// set it earlier; nil for the legacy path. Append it between the
	// build exec and the final container shaping so SSH is gone before
	// withEntrypoint / withoutMount lock the runtime container shape.
	runtimeSelectors = append(runtimeSelectors, postBuildSelectors...)
	runtimeSelectors = append(runtimeSelectors,
		dagql.Selector{
			Field: "withEntrypoint",
			Args: []dagql.NamedInput{
				{
					Name: "args",
					Value: dagql.ArrayInput[dagql.String]{
						goSDKRuntimePath,
					},
				},
			},
		},
		dagql.Selector{
			Field: "withWorkdir",
			Args: []dagql.NamedInput{
				{
					Name:  "path",
					Value: dagql.NewString(RuntimeWorkdirPath),
				},
			},
		},
		// remove shared cache mounts from final container so module code can't
		// do weird things with them like IPC, etc.
		dagql.Selector{
			Field: "withoutMount",
			Args: []dagql.NamedInput{
				{
					Name:  "path",
					Value: dagql.String("/go/pkg/mod"),
				},
			},
		},
		dagql.Selector{
			Field: "withoutMount",
			Args: []dagql.NamedInput{
				{
					Name:  "path",
					Value: dagql.String("/root/.cache/go-build"),
				},
			},
		},
	)
	if err := dag.Select(ctx, ctr, &ctr, runtimeSelectors...); err != nil {
		return nil, fmt.Errorf("failed to build go runtime binary: %w", err)
	}
```

- [ ] **Step 3: Build & vet**

```bash
go build ./core/sdk/... && go vet ./core/sdk/...
```

Expected: both succeed. The `Runtime()` body is now ~10 lines longer; behavior for the legacy path is identical (`postBuildSelectors` is nil, the append is a no-op, the selector order is unchanged).

- [ ] **Step 4: Run the package's existing tests**

```bash
go test ./core/sdk/...
```

Expected: pass. Only existing tests live here (`loader_test.go`); this patch adds none.

- [ ] **Step 5: Refresh**

```bash
stg refresh
stg show --stat
```

Expected: `core/sdk/go_sdk.go | ~170 ++++++-----` cumulative (additions plus the rewritten Runtime block).

---

## Task 6: Wider build / vet sweep + finalize message

**Files:** none changed.

- [ ] **Step 1: Build & vet broader scope**

```bash
go build ./... && go vet ./...
```

Expected: both succeed. If either fails, the issue is almost certainly elsewhere and pre-existing — inspect before proceeding.

- [ ] **Step 2: Sanity-check `go_sdk.go` shape**

```bash
grep -n "func .*useRuntimeCodegen\|func .*requireGeneratedFiles\|func .*baseWithoutCodegen\|func .*baseWithCodegen\|func .*Runtime(" core/sdk/go_sdk.go
```

Expected: in this order — `Runtime(`, `useRuntimeCodegen`, `requireGeneratedFiles`, `baseWithoutCodegen`, `baseWithCodegen`. (`Runtime` stays in its current position around line 454; the three new helpers sit between `Runtime` and `baseWithCodegen`.)

- [ ] **Step 3: Finalize the patch commit message**

```bash
stg edit
```

Replace the message body with:

```
core/sdk/go_sdk: skip codegen at runtime when opted in

When dagger.json has codegen.legacyCodegenAtRuntime=false, Go SDK's
Runtime() skips `codegen generate-module` and trusts the committed
dagger.gen.go + internal/dagger/dagger.gen.go from the user's
context directory. `go build` compiles the module straight from disk.

Implementation: a sibling baseWithoutCodegen helper next to the
legacy baseWithCodegen; a useRuntimeCodegen predicate picks between
them inside Runtime(). The new helper replicates gitconfig + SSH +
GOPRIVATE selectors so private-module Go builds keep working, and
returns the SSH-unset pair to the caller so it can bracket the
go-build exec (the build is the first download trigger on this path).

A small requireGeneratedFiles precheck returns an actionable error
naming the missing file and pointing at `dagger develop` to
(re)generate, covering both first-time opt-in and post-modification
cases with one message.

Codegen() (dagger develop) and ModuleTypes() are untouched. Other
SDKs ignore the flag for now. core/integration tests land in a
follow-up patch.

Signed-off-by: Yves Brissaud <yves@dagger.io>
```

Save & exit. Make sure no `Co-Authored-By` trailer is present.

- [ ] **Step 4: Confirm final state**

```bash
stg series --all
stg show --stat
```

Expected stack:

```
+ schematools
+ legacy-codegen-flag
+ go-sdk-skip-codegen-runtime-design
> go-sdk-runtime-skip-codegen   ← this patch
! schematool-library
```

Expected diff: `core/sdk/go_sdk.go` is the only modified file, ~170 lines added.

---

## Out-of-scope follow-ups (do NOT include in this patch)

- Integration tests under `core/integration` covering the new path.
- `workspaceModuleInit` writing `legacyCodegenAtRuntime: false` when initializing a new `--sdk=go` module.
- Honoring the flag in other SDKs.
- `ModuleTypes()` changes.

Each of these gets its own patch on a follow-up.
