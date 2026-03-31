# Replace `dagger develop` with `dagger generate`

## Status: Draft

## References

- GitHub issue about committing generated files: https://github.com/dagger/dagger/issues/11467
- Prior art: `workspace-generate` branch (implemented on top of workspaces; this design restarts fresh from main)

## Problem

`dagger develop` is confusing. It creates files that are useful for local development
(IDE support, type hints), but those files are git-ignored and not used when calling
functions on a module. The SDK runtime re-generates them internally every time a module
is loaded — a parallel universe where the files on disk and the files the engine uses
are independent.

This causes several problems:
1. **Wasted work**: codegen runs twice — once by `dagger develop` for disk, once by the
   SDK runtime internally.
2. **Confusion**: users don't understand why files are generated but ignored.
3. **No CI verification**: generated files can't be checked into CI because they're
   ephemeral.
4. **Drift**: the files on disk (from `dagger develop`) and the files the engine uses
   (from internal codegen) can diverge silently.

## Goals

1. `dagger generate` replaces `dagger develop` for generating SDK code.
2. Generated files are committed to VCS (not git-ignored).
3. Go, TypeScript, and Python SDKs skip internal re-generation at runtime when generated
   files exist — just build and run.
4. Smooth transition: existing modules keep working; new modules get the new behavior by
   default.
5. A generic compat develop module bridges all existing SDKs to the new `@generate` /
   `Changeset` model without per-SDK logic.

## Non-Goals

- Native SDK-specific develop modules (e.g., `sdk:go:develop`) — follow-up feature.
- Changes to how `dagger call`, `dagger check`, or other commands work.
- Workspace integration beyond using the existing `Workspace` type as a parameter.

## Design

### Compat Develop Module

The compat develop module is a **generic bridge** that makes any existing SDK work with
`dagger generate` without requiring the SDK to create a dedicated develop module.

**Location:** `sdk/compat/develop/`
**Builtin ref:** `sdk:compat:develop`
**Language:** Dang (uses the native embedded Dang runtime; no codegen needed, avoiding
chicken-and-egg problems)

**`dagger.json`:**
```json
{
  "name": "develop",
  "engineVersion": "v0.20.3",
  "sdk": { "source": "dang" }
}
```

**`main.dang` — `+generate` function:**

```dang
pub generate(workspace: Workspace!): Changeset! @generate {
  let root = workspace.directory("/")
  let ws = workspace.directory(".")
  let wsPath = workspace.root

  # Create a ModuleSource from the root directory with the workspace path
  # as the source root. This lets the engine resolve relative dependency
  # paths that escape the module's own source directory.
  let modSrc = root.asModuleSource(sourceRootPath: wsPath)

  # Run the SDK's Codegen function via the engine pipeline.
  let genDir = modSrc.generatedContextDirectory

  # Extract only the module's subdirectory from the generated output.
  let generated = genDir.directory(wsPath)

  # Overlay generated code onto the module directory.
  let result = ws.withDirectory(".", generated)

  # Clean .gitignore: use .gitattributes as the source of truth for which
  # files are generated. Remove .gitignore entries that match .gitattributes
  # paths, so generated files are no longer ignored and can be committed.
  let cleaned = container
    .from("alpine:3.22")
    .withMountedDirectory("/work", result)
    .withWorkdir("/work")
    .withExec([
      "sh", "-c",
      "if [ -f .gitattributes ] && [ -f .gitignore ]; then " + "  awk '{print $1}' .gitattributes | while read -r pat; do " + "    sed -i \"\\|^${pat}$|d\" .gitignore; " + "    pat_noglob=$(echo \"$pat\" | sed 's|/\\*\\*$||'); " + "    sed -i \"\\|^${pat_noglob}$|d\" .gitignore; " + "  done; " + "fi",
    ])
    .directory("/work")

  # Update dagger.json: set codegen.automaticGitignore to false
  let falseVal = json.newBoolean(value: false)
  let updatedConfig = cleaned
    .file("dagger.json")
    .asJSON
    .withField(path: ["codegen", "automaticGitignore"], value: falseVal)
  let result = cleaned.withNewFile("dagger.json", updatedConfig.asString)

  result.changes(ws)
}
```

The `+generate` function:
1. Receives the module's `Workspace` (auto-injected by the engine).
2. Creates a `ModuleSource` from the workspace root so relative deps resolve correctly.
3. Calls `generatedContextDirectory` — this triggers the SDK's existing `Codegen()`
   interface internally.
4. Overlays generated files onto the module directory.
5. Cleans `.gitignore`: reads `.gitattributes` to identify generated patterns, removes
   matching entries from `.gitignore` so files can be committed.
6. Removes `legacyRuntimeCodegen` from `dagger.json` if present (the module is now on the
   new path).
7. Returns the result as a `Changeset`.

**`+check` function (`generated`):**

```dang
pub generated(workspace: Workspace!): Void @check {
  let changes = generate(workspace: workspace)
  if (!changes.isEmpty) {
    container.withError("generated files are not up to date, run 'dagger generate' to fix").sync
  }
  null
}
```

Runs `+generate` and verifies the changeset is empty. If not, generated files are stale.
This gives users a CI-friendly check.

### Builtin Module Refs

SDK develop modules use a **builtin module ref** format: `sdk:<language>:<module>`, e.g.
`sdk:compat:develop`. This is analogous to how SDKs have short names (`go`, `python`,
`typescript`) — a well-known identifier that the engine resolves from embedded content,
not from git or the local filesystem.

**Format:** `sdk:<language>:<module>` — colons distinguish it from paths and git refs.

**Resolution:** The engine embeds each module's source tree as an OCI tarball at build
time, following the same pattern used for the Python and TypeScript SDK embedding. The
OCI rootfs mirrors the repo directory structure so that relative dependency paths in
`dagger.json` resolve correctly.

**Resolution chain:**
1. Module ref parser sees the `sdk:` prefix → treats it as a builtin ref.
2. Engine looks up `<lang>:<module>` in a registry of embedded OCI tarballs.
3. Loads the OCI container from the engine's content store via `_builtinContainer`.
4. Creates a `ModuleSourceKindDir` with the rootfs as context directory and the module at
   its subpath (e.g., `sdk/compat/develop`).
5. Relative deps in `dagger.json` resolve within the rootfs (relevant for future native
   develop modules with deps like `cmd/codegen`).

**Engine embedding (build time):**
- `toolchains/engine-dev/build/sdk.go` packages the module source into an OCI tarball.
- The tarball's manifest digest is stored as an env var (e.g.,
  `DAGGER_COMPAT_SDK_DEVELOP_MANIFEST_DIGEST`) in the engine container.

**Loader changes (`core/sdk/loader.go`):**
- New function to parse `sdk:` prefix and extract `<lang>` and `<module>`.
- A registry mapping `(lang, module)` → manifest digest env var name.
- Reuses `loadBuiltinSDK()`-style loading but for modules (not SDK runtimes).

**Path handling:**
- CLI path transformation (local path resolution) must be skipped for `sdk:` refs.
- `modules.IsLocalSource()` must return false for `sdk:` refs.

**Initial builtin module refs:**

| Ref                  | Source path           | Content                        |
|----------------------|-----------------------|--------------------------------|
| `sdk:compat:develop` | `sdk/compat/develop/` | Generic compat develop module  |

The registry is designed to be extensible. Adding `sdk:go:develop` later means embedding
another tarball and registering it — no architectural changes needed.

### Transition: `legacyRuntimeCodegen` Flag

A new top-level field in `dagger.json` marks modules that use the old "develop" workflow:

```go
// If true, generated code is treated as ephemeral and re-generated at runtime
// (legacy "dagger develop" behavior). When absent or false on engine > v0.20.3
// with Go/Python/TypeScript SDKs, the engine expects generated files to be
// committed and present in the source.
LegacyRuntimeCodegen *bool `json:"legacyRuntimeCodegen,omitempty"`
```

**Who sets it:**
- `dagger develop` → sets `legacyRuntimeCodegen: true` (stamps legacy mode).
- `dagger init --sdk` → does NOT set it (new modules start in new mode).
- Compat develop module's `+generate` → removes `legacyRuntimeCodegen` if present
  (the module is now on the new path).

**Version-gating logic (in SDK `Runtime()` / `ModuleTypes()`):**

```
if legacyRuntimeCodegen == true:
    → old behavior: re-generate internally (always)
else if engine version <= v0.20.3:
    → old behavior: re-generate internally (backward compat)
else if SDK is Go, Python, or TypeScript:
    → new behavior: expect generated files to exist, just build and run
else:
    → old behavior: re-generate internally (other SDKs unchanged)
```

**Error case:** If a module is on the new path but generated files are missing (e.g.,
fresh clone without running `dagger generate`), the SDK fails with a clear error:
"generated files not found, run `dagger generate` first".

### `.gitignore` Handling

The shift from "generated files are ephemeral" to "generated files are committed" requires
cleaning up `.gitignore` entries that the old `dagger develop` added.

**Current behavior (`dagger develop`):**
- SDK's `Codegen()` returns `VCSIgnoredPaths` (e.g., Go returns `dagger.gen.go`,
  `internal/dagger`, `internal/querybuilder`, `internal/telemetry`, `.env`).
- The engine writes these paths into `.gitignore` in the module's source directory.
- Controlled by `ModuleCodegenConfig.AutomaticGitignore` in `dagger.json`.

**New behavior (`dagger generate` via compat module):**
- The compat module calls the old `Codegen()` path internally, then actively removes
  `.gitignore` entries that match `.gitattributes` patterns (generated files).
- The compat module sets `codegen.automaticGitignore: false` in `dagger.json`, so
  the engine skips `.gitignore` writes if `dagger develop` is used later.
- `.gitattributes` entries (`VCSGeneratedPaths`) are preserved — marking files as
  "generated" in code review UIs is still useful for committed files.

### SDK Runtime Optimization

For Go, TypeScript, and Python SDKs, when `legacyRuntimeCodegen` is absent/false and
engine > v0.20.3, the SDK skips internal re-generation at runtime.

**Go SDK (`core/sdk/go_sdk.go`):**
- `Runtime()` currently runs `codegen generate-module` then `go build`. With the
  optimization: skip the `codegen generate-module` step, just `go build` directly
  using the source files as-is.
- `ModuleTypes()` currently runs `codegen generate-typedefs` to introspect Go source.
  This still needs to run — it's type extraction, not file generation. No change here.

**Python / TypeScript SDKs (module-based):**
- These are loaded via `loadBuiltinSDK()` as external modules. Their `Runtime()` and
  `ModuleTypes()` implementations live in the SDK modules themselves.
- The optimization is internal to each SDK module: detect that generated files are
  present and skip the codegen step when building the runtime container.
- The `ModuleSource` already carries the parsed `dagger.json` config, including
  `legacyRuntimeCodegen`, so the SDK can check the flag.

**Error handling:** If the SDK is on the new path but generated files are missing, it
fails with: "generated files not found, run `dagger generate` first".

### CLI Command Changes

**`dagger generate` (unhide):**
- Remove `Hidden: true` from `generateCmd` in `cmd/dagger/generators.go`.
- No other changes to the command — existing behavior is correct: discovers `@generate`
  functions from toolchains, runs them, applies changesets.

**`dagger develop` (deprecate):**
- Print deprecation warning: "dagger develop is deprecated, use dagger generate instead."
- Stamp `legacyRuntimeCodegen: true` in `dagger.json` after running.
- Otherwise behavior unchanged — still calls `GeneratedContextChangeset()`, exports,
  handles license creation.

**`dagger init --sdk <name>` (add compat toolchain):**
- When `--sdk` is provided, automatically add `sdk:compat:develop` as a toolchain.
- This makes `dagger generate` work out of the box for new modules.

**No changes to:** `dagger call`, `dagger check`, `dagger functions`,
`dagger toolchain install`.

### Migration Path for Existing Users

1. **Do nothing**: `dagger develop` keeps working. Next time the user runs it, it stamps
   `legacyRuntimeCodegen: true`. Module behavior is unchanged.

2. **Opt in to `dagger generate`**:
   - Run `dagger toolchain install sdk:compat:develop` to add the compat module.
   - Run `dagger generate` — this generates files, cleans `.gitignore`, removes
     `legacyRuntimeCodegen`, sets `codegen.automaticGitignore: false`.
   - Commit the generated files.
   - From now on, use `dagger generate` instead of `dagger develop`.
   - The SDK skips internal re-generation at runtime (Go/TS/Python).

3. **Future: native develop modules**: When `sdk:go:develop` is available, replace the
   compat toolchain with it for Go-specific optimizations.
