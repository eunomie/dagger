# Replace `dagger develop` with `dagger generate` — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `dagger develop` with `dagger generate` as the primary command for generating SDK code, making generated files committable and eliminating runtime re-generation for Go/TS/Python SDKs.

**Architecture:** A compat develop module (`sdk:compat:develop`) written in Dang bridges existing SDKs to the `@generate`/`Changeset` model. It's embedded in the engine via an OCI tarball and resolved through a generic `sdk:<lang>:<module>` builtin ref system. A `legacyRuntimeCodegen` flag in `dagger.json` gates the transition. SDK `Runtime()` methods skip internal codegen when the flag is absent.

**Tech Stack:** Go (engine, CLI, Go SDK), Dang (compat module), OCI (embedding)

**Spec:** `hack/designs/generate-module-codegen.md`

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `core/modules/config.go` | Modify | Add `LegacyRuntimeCodegen` field to `ModuleConfig` |
| `core/modulesource.go` | Modify | Expose `LegacyRuntimeCodegen` on `ModuleSource` |
| `core/modulerefs.go` | Modify | Handle `sdk:` prefix in `fastModuleSourceKindCheck()` |
| `core/schema/modulesource.go` | Modify | Add `sdk:` builtin ref resolution in `moduleSource()` |
| `engine/distconsts/consts.go` | Modify | Add `CompatSDKDevelopManifestDigestEnvName` |
| `toolchains/engine-dev/build/sdk.go` | Modify | Add `compatSDKDevelopContent()` to embed compat module |
| `toolchains/engine-dev/build/builder.go` | Modify | Register compat SDK in engine build |
| `sdk/compat/develop/dagger.json` | Create | Module config for compat develop module |
| `sdk/compat/develop/main.dang` | Create | Compat develop module Dang source |
| `cmd/dagger/generators.go` | Modify | Unhide `dagger generate` command |
| `cmd/dagger/module.go` | Modify | Deprecate `dagger develop`, add compat toolchain to `dagger init --sdk` |
| `core/sdk/go_sdk.go` | Modify | Skip codegen in `Runtime()` when `legacyRuntimeCodegen` is absent |
| `core/schema/modulesource.go` | Modify | Stamp `legacyRuntimeCodegen` in `dagger develop` path |

---

## Task 1: Add `legacyRuntimeCodegen` to `ModuleConfig`

**Files:**
- Modify: `core/modules/config.go:49-87` — `ModuleConfig` struct
- Test: `core/modules/config_test.go` (if exists, otherwise create)

- [ ] **Step 1: Write test for parsing `legacyRuntimeCodegen` from JSON**

In a test file, verify that `ModuleConfig` round-trips the new field:

```go
func TestModuleConfigLegacyRuntimeCodegen(t *testing.T) {
    jsonData := `{"name":"test","engineVersion":"v0.20.4","legacyRuntimeCodegen":true}`
    var cfg ModuleConfig
    err := json.Unmarshal([]byte(jsonData), &cfg)
    require.NoError(t, err)
    require.NotNil(t, cfg.LegacyRuntimeCodegen)
    require.True(t, *cfg.LegacyRuntimeCodegen)

    // Round-trip
    out, err := json.Marshal(cfg)
    require.NoError(t, err)
    require.Contains(t, string(out), `"legacyRuntimeCodegen":true`)

    // Absent case
    jsonData2 := `{"name":"test","engineVersion":"v0.20.4"}`
    var cfg2 ModuleConfig
    err = json.Unmarshal([]byte(jsonData2), &cfg2)
    require.NoError(t, err)
    require.Nil(t, cfg2.LegacyRuntimeCodegen)

    out2, err := json.Marshal(cfg2)
    require.NoError(t, err)
    require.NotContains(t, string(out2), "legacyRuntimeCodegen")
}
```

- [ ] **Step 2: Run test — expect FAIL**

Run: `go test ./core/modules/ -run TestModuleConfigLegacyRuntimeCodegen -v`
Expected: compilation error — `LegacyRuntimeCodegen` field doesn't exist

- [ ] **Step 3: Add the field to `ModuleConfig`**

In `core/modules/config.go`, add after the `DisableDefaultFunctionCaching` field (line 86):

```go
// If true, generated code is treated as ephemeral and re-generated at runtime
// (legacy "dagger develop" behavior). When absent or false on engine > v0.20.3
// with Go/Python/TypeScript SDKs, the engine expects generated files to be
// committed and present in the source.
LegacyRuntimeCodegen *bool `json:"legacyRuntimeCodegen,omitempty"`
```

- [ ] **Step 4: Run test — expect PASS**

Run: `go test ./core/modules/ -run TestModuleConfigLegacyRuntimeCodegen -v`

- [ ] **Step 5: Commit**

```
feat: add legacyRuntimeCodegen field to ModuleConfig
```

---

## Task 2: Expose `legacyRuntimeCodegen` on `ModuleSource`

**Files:**
- Modify: `core/modulesource.go:138-191` — `ModuleSource` struct
- Modify: `core/schema/modulesource.go` — where `ModuleSource` fields are loaded from config

- [ ] **Step 1: Add field to `ModuleSource` struct**

In `core/modulesource.go`, add to the `ModuleSource` struct:

```go
LegacyRuntimeCodegen *bool
```

- [ ] **Step 2: Wire it from config loading**

In `core/schema/modulesource.go`, find where `loadModuleSourceConfig()` populates `ModuleSource` fields from `ModuleConfig`. Add:

```go
src.LegacyRuntimeCodegen = modCfg.LegacyRuntimeCodegen
```

Search for the function `loadModuleSourceConfig` and trace where config fields are mapped to source fields. The pattern will be evident from how `DisableDefaultFunctionCaching` is handled.

- [ ] **Step 3: Verify build compiles**

Run: `go build ./core/...`

- [ ] **Step 4: Commit**

```
feat: expose legacyRuntimeCodegen on ModuleSource
```

---

## Task 3: Handle `sdk:` prefix in module ref parsing

**Files:**
- Modify: `core/modulerefs.go:20-45` — `fastModuleSourceKindCheck()`
- Modify: `core/modulerefs.go:53-80` — `ParseRefString()`
- Modify: `core/modulesource.go:33-44` — `ModuleSourceKind` constants
- Modify: `core/schema/modulesource.go:304-334` — `moduleSource()` resolver
- Test: `core/modulerefs_test.go`

The `sdk:` prefix needs a new module source kind so the engine can resolve it from embedded content instead of the filesystem or git.

- [ ] **Step 1: Write test for `sdk:` prefix detection**

In `core/modulerefs_test.go`, add test cases for `fastModuleSourceKindCheck`:

```go
// Test that sdk: prefix is recognized as a builtin ref
func TestFastModuleSourceKindCheckBuiltin(t *testing.T) {
    require.Equal(t, ModuleSourceKindBuiltin, fastModuleSourceKindCheck("sdk:compat:develop", ""))
    require.Equal(t, ModuleSourceKindBuiltin, fastModuleSourceKindCheck("sdk:go:develop", ""))
    // Non-sdk: prefixes should NOT match
    require.NotEqual(t, ModuleSourceKindBuiltin, fastModuleSourceKindCheck("./sdk/local", ""))
    require.NotEqual(t, ModuleSourceKindBuiltin, fastModuleSourceKindCheck("github.com/foo/sdk", ""))
}
```

- [ ] **Step 2: Run test — expect FAIL**

Run: `go test ./core/ -run TestFastModuleSourceKindCheckBuiltin -v`
Expected: `ModuleSourceKindBuiltin` not defined

- [ ] **Step 3: Add `ModuleSourceKindBuiltin` constant**

In `core/modulesource.go`, add to the `ModuleSourceKind` enum registration (lines 37-44).
The existing kinds use `ModuleSourceKindEnum.Register()`:

```go
ModuleSourceKindBuiltin = ModuleSourceKindEnum.Register("BUILTIN_SOURCE")
_                       = ModuleSourceKindEnum.AliasView("BUILTIN", "BUILTIN_SOURCE", enumView)
```

Also add a case to `HumanString()` (around line 95):

```go
case ModuleSourceKindBuiltin:
    return "builtin"
```

- [ ] **Step 4: Add `sdk:` detection in `fastModuleSourceKindCheck`**

In `core/modulerefs.go:20-45`, add a case at the top of the switch (before the `refPin` check):

```go
case strings.HasPrefix(refString, "sdk:"):
    return ModuleSourceKindBuiltin
```

- [ ] **Step 5: Add builtin handling in `ParseRefString`**

In `core/modulerefs.go`, add a case for `ModuleSourceKindBuiltin` in the switch inside `ParseRefString()`:

```go
case ModuleSourceKindBuiltin:
    return &ParsedRefString{
        Kind: kind,
        Builtin: &ParsedBuiltinRefString{
            Ref: refString,
        },
    }, nil
```

Add the new struct:

```go
type ParsedBuiltinRefString struct {
    Ref string // e.g. "sdk:compat:develop"
}
```

And add the `Builtin` field to `ParsedRefString`:

```go
type ParsedRefString struct {
    Kind    ModuleSourceKind
    Local   *ParsedLocalRefString
    Git     *ParsedGitRefString
    Builtin *ParsedBuiltinRefString
}
```

- [ ] **Step 6: Run tests — expect PASS**

Run: `go test ./core/ -run TestFastModuleSourceKindCheckBuiltin -v`

- [ ] **Step 7: Commit**

```
feat: add ModuleSourceKindBuiltin for sdk: prefix refs
```

---

## Task 4: Resolve `sdk:` builtin refs in the engine

**Files:**
- Modify: `engine/distconsts/consts.go:17-21` — add compat env name
- Modify: `core/schema/modulesource.go:304-334` — `moduleSource()` resolver
- Modify: `core/schema/host.go` — reuse `_builtinContainer` pattern

This is the core engine change: when `moduleSource()` sees a `ModuleSourceKindBuiltin` ref, it loads the embedded OCI tarball and creates a `ModuleSourceKindDir`.

- [ ] **Step 1: Add env name constant**

In `engine/distconsts/consts.go`, add:

```go
CompatSDKDevelopManifestDigestEnvName = "DAGGER_COMPAT_SDK_DEVELOP_MANIFEST_DIGEST"
```

- [ ] **Step 2: Create builtin module registry**

In a new file `core/schema/builtinmodules.go`, create:

```go
package schema

import (
    "fmt"
    "os"
    "strings"

    "github.com/opencontainers/go-digest"

    "github.com/dagger/dagger/engine/distconsts"
)

// builtinModuleRef parses an "sdk:<lang>:<module>" ref string and returns
// the OCI manifest digest for the embedded module, plus the subpath within
// the tarball's rootfs where the module lives.
//
// Returns ("", "", error) if the ref is not a known builtin.
func builtinModuleRef(ref string) (manifestDigest digest.Digest, subpath string, err error) {
    // strip "sdk:" prefix
    rest := strings.TrimPrefix(ref, "sdk:")
    parts := strings.SplitN(rest, ":", 2)
    if len(parts) != 2 {
        return "", "", fmt.Errorf("invalid builtin module ref %q: expected sdk:<lang>:<module>", ref)
    }
    lang, module := parts[0], parts[1]

    key := lang + ":" + module
    entry, ok := builtinModuleRegistry[key]
    if !ok {
        return "", "", fmt.Errorf("unknown builtin module ref %q", ref)
    }

    dgstStr := os.Getenv(entry.envName)
    if dgstStr == "" {
        return "", "", fmt.Errorf("builtin module %q not embedded in engine (env %s not set)", ref, entry.envName)
    }

    dgst, err := digest.Parse(dgstStr)
    if err != nil {
        return "", "", fmt.Errorf("invalid digest for builtin module %q: %w", ref, err)
    }

    return dgst, entry.subpath, nil
}

type builtinModuleEntry struct {
    envName string
    subpath string // path within the OCI rootfs
}

var builtinModuleRegistry = map[string]builtinModuleEntry{
    "compat:develop": {
        envName: distconsts.CompatSDKDevelopManifestDigestEnvName,
        subpath: "sdk/compat/develop",
    },
    // Future: "go:develop", "python:develop", etc.
}
```

- [ ] **Step 3: Add builtin resolution in `moduleSource()` resolver**

In `core/schema/modulesource.go`, in the `moduleSource()` function (around line 322), add a case for the new kind:

```go
case core.ModuleSourceKindBuiltin:
    inst, err = s.builtinModuleSource(ctx, query, parsedRef.Builtin.Ref)
    if err != nil {
        return inst, err
    }
```

Then implement the `builtinModuleSource` method. This should:
1. Call `builtinModuleRef()` to get the digest and subpath.
2. Use `_builtinContainer(digest)` to load the OCI container.
3. Get its `rootfs` as a directory.
4. Create a `ModuleSourceKindDir` from that rootfs with the subpath.

Follow the pattern in `core/sdk/loader.go:loadBuiltinSDK()` (lines 148-206) for how `_builtinContainer` is used. The key dagql selectors are:

```go
// 1. Load the builtin container
dag.Select(ctx, query, &ctr, dagql.Selector{
    Field: "_builtinContainer",
    Args: []dagql.NamedInput{
        {Name: "digest", Value: dagql.String(manifestDigest.String())},
    },
})

// 2. Get its rootfs
dag.Select(ctx, ctr, &rootfs, dagql.Selector{Field: "rootfs"})

// 3. Create module source from the rootfs directory
dag.Select(ctx, rootfs, &inst, dagql.Selector{
    Field: "asModuleSource",
    Args: []dagql.NamedInput{
        {Name: "sourceRootPath", Value: dagql.String(subpath)},
    },
})
```

- [ ] **Step 4: Verify build compiles**

Run: `go build ./core/...`

- [ ] **Step 5: Commit**

```
feat: resolve sdk: builtin module refs from embedded OCI tarballs
```

---

## Task 5: Create the compat develop module

**Files:**
- Create: `sdk/compat/develop/dagger.json`
- Create: `sdk/compat/develop/main.dang`

- [ ] **Step 1: Create `sdk/compat/develop/dagger.json`**

```json
{
  "name": "develop",
  "sdk": { "source": "dang" }
}
```

- [ ] **Step 2: Create `sdk/compat/develop/main.dang`**

```dang
pub description = "SDK compatibility layer — generates code for any SDK via the engine's codegen pipeline"

type Develop {
  """
  Generate SDK code for a Dagger module.
  Delegates to the engine's existing SDK codegen pipeline, so it works
  for any SDK (Go, Python, TypeScript, etc.) without per-SDK logic.
  """
  pub generate(workspace: Workspace!): Changeset! @generate {
    # Use the sandbox root so that modules with external dependencies
    # (e.g. "../engine-dev") can resolve paths outside their own source.
    let root = workspace.directory("/")
    let ws = workspace.directory(".")
    let wsPath = workspace.path

    # Create a ModuleSource from the root directory with the workspace path
    # as the source root. This lets the engine resolve relative dependency
    # paths that escape the module's own source directory.
    let modSrc = root.asModuleSource(sourceRootPath: wsPath)

    # Run the SDK's Codegen function via the engine pipeline. This returns
    # a directory with files at context-root-relative paths.
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
      .withExec(["sh", "-c",
        "if [ -f .gitattributes ] && [ -f .gitignore ]; then " +
        "  awk '{print $1}' .gitattributes | while read -r pat; do " +
        "    sed -i \"\\|^${pat}$|d\" .gitignore; " +
        "    pat_noglob=$(echo \"$pat\" | sed 's|/\\*\\*$||'); " +
        "    sed -i \"\\|^${pat_noglob}$|d\" .gitignore; " +
        "  done; " +
        "fi"
      ])
      .directory("/work")

    # Update dagger.json:
    # 1. Remove legacyRuntimeCodegen if present (module is on the new path)
    # 2. Set codegen.automaticGitignore to false (prevent dagger develop
    #    from re-adding .gitignore entries)
    let config = cleaned.file("dagger.json").asJSON
    let config = if (config.hasField(path: ["legacyRuntimeCodegen"])) {
      config.without(path: ["legacyRuntimeCodegen"])
    } else {
      config
    }
    let config = config.with(path: ["codegen", "automaticGitignore"], value: false)
    let cleaned = cleaned.withNewFile("dagger.json", config.asString)

    cleaned.changes(ws)
  }

  """
  Check that generated SDK code is up to date
  """
  pub generated(workspace: Workspace!): Void @check {
    let changes = generate(workspace: workspace)
    if (!changes.isEmpty) {
      container.withError("generated files are not up to date, run 'dagger generate' to fix").sync
    }
    null
  }
}
```

- [ ] **Step 3: Commit**

```
feat: create compat develop module (sdk:compat:develop)
```

---

## Task 6: Embed compat module in the engine image

**Files:**
- Modify: `toolchains/engine-dev/build/sdk.go` — add `compatSDKDevelopContent()`
- Modify: `toolchains/engine-dev/build/builder.go:87` — register in SDK list

- [ ] **Step 1: Add `compatSDKDevelopContent()` in `sdk.go`**

Add after the `goSDKContent()` function (after line 251). The compat module is simple — just source files, no pre-built binaries:

```go
func (build *Builder) compatSDKDevelopContent(ctx context.Context) (*sdkContent, error) {
    rootfs := dag.Directory().
        WithDirectory("sdk/compat/develop", build.source.Directory("sdk/compat/develop"))

    sdkCtrTarball := dag.Container().
        WithRootfs(rootfs).
        AsTarball(dagger.ContainerAsTarballOpts{
            ForcedCompression: dagger.ImageLayerCompressionZstd,
        })
    sdkDir := unpackTar(sdkCtrTarball)

    var index ocispecs.Index
    indexContents, err := sdkDir.File("index.json").Contents(ctx)
    if err != nil {
        return nil, err
    }
    if err := json.Unmarshal([]byte(indexContents), &index); err != nil {
        return nil, err
    }

    return &sdkContent{
        index:   index,
        sdkDir:  sdkDir,
        envName: distconsts.CompatSDKDevelopManifestDigestEnvName,
    }, nil
}
```

- [ ] **Step 2: Register in engine build**

In `toolchains/engine-dev/build/builder.go`, modify line 87 to include the compat SDK:

```go
sdks := []sdkContentF{build.goSDKContent, build.pythonSDKContent, build.typescriptSDKContent, build.compatSDKDevelopContent}
```

- [ ] **Step 3: Verify build compiles**

Run: `go build ./toolchains/engine-dev/...`

- [ ] **Step 4: Commit**

```
feat: embed compat develop module in engine image
```

---

## Task 7: Unhide `dagger generate` command

**Files:**
- Modify: `cmd/dagger/generators.go:31-32` — remove `Hidden: true`

- [ ] **Step 1: Remove `Hidden: true`**

In `cmd/dagger/generators.go`, line 32, remove `Hidden: true` from `generateCmd`:

```go
var generateCmd = &cobra.Command{
    Use:    "generate [options] [pattern...]",
    // (Hidden: true line removed)
    Short:  "Generate assets of your project",
```

- [ ] **Step 2: Verify build compiles**

Run: `go build ./cmd/dagger/`

- [ ] **Step 3: Commit**

```
feat: unhide dagger generate command
```

---

## Task 8: Add compat toolchain to `dagger init --sdk`

**Files:**
- Modify: `cmd/dagger/module.go:280-348` — `moduleInitCmd` handler

When `--sdk` is provided, add `sdk:compat:develop` as a toolchain before exporting.

- [ ] **Step 1: Add toolchain installation after SDK setup**

In `cmd/dagger/module.go`, after the `modSrc = modSrc.WithSDK(sdk)` line (around line 305), add:

```go
if sdk != "" {
    modSrc = modSrc.WithSDK(sdk)
    // Add the compat develop module as a toolchain so dagger generate works
    compatToolchain := dag.ModuleSource("sdk:compat:develop", dagger.ModuleSourceOpts{
        DisableFindUp: true,
    }).WithName("develop")
    modSrc = modSrc.WithToolchains([]*dagger.ModuleSource{compatToolchain})
}
```

This matches the existing pattern at `cmd/dagger/module.go:821` where `dagger toolchain install` calls `modSrc.WithToolchains([]*dagger.ModuleSource{toolchainSrc})`.

- [ ] **Step 2: Update help text**

Change the `Long` description (line 205) from referencing `dagger develop` to `dagger generate`:

```go
"If --sdk is specified, the given SDK is installed in the module. You can do this later with \"dagger generate\"."
```

- [ ] **Step 3: Verify build compiles**

Run: `go build ./cmd/dagger/`

- [ ] **Step 4: Commit**

```
feat: add sdk:compat:develop toolchain on dagger init --sdk
```

---

## Task 9: Deprecate `dagger develop`

**Files:**
- Modify: `cmd/dagger/module.go:593-769` — `moduleDevelopCmd`

- [ ] **Step 1: Add deprecation warning**

At the beginning of `moduleDevelopCmd.RunE` (around line 621), add:

```go
fmt.Fprintln(cmd.ErrOrStderr(),
    "WARNING: dagger develop is deprecated. Use dagger generate instead.",
    "\nSee https://docs.dagger.io/reference/cli/#dagger-generate for migration instructions.")
```

- [ ] **Step 2: Stamp `legacyRuntimeCodegen: true` after running**

After the successful export of generated code (after line 752), read the `dagger.json` that was just written, add the field, and write it back:

```go
// Stamp legacyRuntimeCodegen: true after export
daggerJSONPath := filepath.Join(srcRootPath, "dagger.json")
daggerJSON, err := os.ReadFile(daggerJSONPath)
if err != nil {
    return fmt.Errorf("failed to read dagger.json: %w", err)
}
var cfg modules.ModuleConfig
if err := json.Unmarshal(daggerJSON, &cfg); err != nil {
    return fmt.Errorf("failed to parse dagger.json: %w", err)
}
legacyTrue := true
cfg.LegacyRuntimeCodegen = &legacyTrue
updatedJSON, err := json.MarshalIndent(cfg, "", "  ")
if err != nil {
    return fmt.Errorf("failed to marshal dagger.json: %w", err)
}
if err := os.WriteFile(daggerJSONPath, updatedJSON, 0644); err != nil {
    return fmt.Errorf("failed to write dagger.json: %w", err)
}
```

Note: `dagger.json` uses `modules.ModuleConfigUserFields` for round-tripping (preserves `$schema` etc.). Check if there's a combined struct that includes both `ModuleConfig` and `ModuleConfigUserFields` for proper round-tripping. Look at how the existing code at `core/modules/config.go` handles this.

- [ ] **Step 3: Add `Deprecated` field to command**

```go
var moduleDevelopCmd = &cobra.Command{
    Deprecated: "use \"dagger generate\" instead",
    Use:        "develop [options]",
```

Note: Cobra's `Deprecated` field automatically prints a deprecation message. You may not need the manual `fmt.Fprintln` if Cobra's message is sufficient. Test both and choose.

- [ ] **Step 4: Verify build compiles and test deprecation output**

Run: `go build ./cmd/dagger/`

- [ ] **Step 5: Commit**

```
feat: deprecate dagger develop in favor of dagger generate
```

---

## Task 10: Go SDK runtime optimization — skip codegen when files exist

**Files:**
- Modify: `core/sdk/go_sdk.go:391-474` — `Runtime()` method
- Modify: `core/sdk/go_sdk.go:476-560` — `baseWithCodegen()` method

- [ ] **Step 1: Add helper to check if runtime codegen should be skipped**

In `core/sdk/go_sdk.go`, add a helper:

```go
// shouldSkipRuntimeCodegen returns true if the module has committed generated
// files and should not re-generate at runtime.
func shouldSkipRuntimeCodegen(src dagql.ObjectResult[*core.ModuleSource]) bool {
    // Legacy mode explicitly requested
    if src.Self().LegacyRuntimeCodegen != nil && *src.Self().LegacyRuntimeCodegen {
        return false
    }
    // Version gate: only skip for engine versions > v0.20.3
    if !engine.CheckVersionCompatibility(src.Self().EngineVersion, "v0.20.4") {
        return false
    }
    return true
}
```

Check that `engine.CheckVersionCompatibility` exists and does what we need (returns true if the version is >= the minimum). Look at `engine/version.go` for the exact API.

- [ ] **Step 2: Modify `baseWithCodegen` to skip codegen when appropriate**

In `baseWithCodegen()` (line 476), add a check at the beginning:

```go
func (sdk *goSDK) baseWithCodegen(
    ctx context.Context,
    deps *core.ModDeps,
    src dagql.ObjectResult[*core.ModuleSource],
) (dagql.ObjectResult[*core.Container], error) {
    if shouldSkipRuntimeCodegen(src) {
        return sdk.baseWithExistingCode(ctx, src)
    }
    // ... existing codegen logic ...
}
```

Then implement `baseWithExistingCode` which mounts the source directory without running codegen:

```go
func (sdk *goSDK) baseWithExistingCode(
    ctx context.Context,
    src dagql.ObjectResult[*core.ModuleSource],
) (dagql.ObjectResult[*core.Container], error) {
    var ctr dagql.ObjectResult[*core.Container]

    dag, err := sdk.root.Server.Server(ctx)
    if err != nil {
        return ctr, err
    }

    contextDir := src.Self().ContextDirectory
    srcSubpath := src.Self().SourceSubpath

    ctr, err = sdk.base(ctx)
    if err != nil {
        return ctr, err
    }

    // Mount the source as-is (generated files already present)
    if err := dag.Select(ctx, ctr, &ctr,
        dagql.Selector{
            Field: "withMountedDirectory",
            Args: []dagql.NamedInput{
                {Name: "path", Value: dagql.NewString(goSDKUserModContextDirPath)},
                {Name: "source", Value: dagql.NewID[*core.Directory](contextDir.ID())},
            },
        },
        dagql.Selector{
            Field: "withWorkdir",
            Args: []dagql.NamedInput{
                {Name: "path", Value: dagql.NewString(
                    filepath.Join(goSDKUserModContextDirPath, srcSubpath),
                )},
            },
        },
    ); err != nil {
        return ctr, err
    }

    return ctr, nil
}
```

- [ ] **Step 3: Verify build compiles**

Run: `go build ./core/...`

- [ ] **Step 4: Commit**

```
feat: Go SDK skips runtime codegen when generated files are committed
```

---

## Task 11: Python SDK runtime optimization

**Files:**
- Modify: `sdk/python/runtime/main.go:147-174` — `Codegen()` method
- Modify: `sdk/python/runtime/main.go:177-200` — `ModuleRuntime()` method

Both the Python and TypeScript SDK runtimes are Go modules that implement the SDK
interface. They are NOT Python/TypeScript code — they're Go code in `sdk/<lang>/runtime/`.

The `ModuleRuntime()` function (line 177) calls `m.Common()` which runs codegen internally
and returns a configured container. The optimization: check `ModuleSource.LegacyRuntimeCodegen`
and skip the internal codegen step when files are already committed.

- [ ] **Step 1: Check how `ModuleSource` config is accessible**

The `ModuleRuntime()` method receives `modSource *dagger.ModuleSource`. Check if
`legacyRuntimeCodegen` is exposed as a field on the generated `ModuleSource` type in
`sdk/python/runtime/dagger.gen.go`. If not, it needs to be added to the GraphQL schema
first (in `core/schema/modulesource.go`).

- [ ] **Step 2: Add skip-codegen logic to `ModuleRuntime()`**

In `sdk/python/runtime/main.go`, modify `ModuleRuntime()` to check the flag. The
`Common()` helper (which runs codegen) should be skipped when files exist. The container
setup still needs to happen — just without re-running the codegen binary.

- [ ] **Step 3: Commit**

```
feat: Python SDK skips runtime codegen when generated files are committed
```

---

## Task 11b: TypeScript SDK runtime optimization

**Files:**
- Modify: `sdk/typescript/runtime/main.go:47-90` — `ModuleRuntime()` method
- Modify: `sdk/typescript/runtime/main.go:92+` — `Codegen()` method

Same pattern as Python. The TypeScript SDK runtime is also a Go module at
`sdk/typescript/runtime/`. `ModuleRuntime()` (line 47) calls `analyzeModuleConfig()`
then runs codegen to set up the container.

- [ ] **Step 1: Check `ModuleSource` config accessibility (same as Python)**

- [ ] **Step 2: Add skip-codegen logic to `ModuleRuntime()`**

- [ ] **Step 3: Commit**

```
feat: TypeScript SDK skips runtime codegen when generated files are committed
```

---

## Task 12: Integration test — end-to-end `dagger generate` flow

**Files:**
- Modify: `core/integration/module_test.go` or create new test file

- [ ] **Step 1: Write integration test**

Create a test that:
1. Runs `dagger init --sdk go` in a temp directory
2. Verifies `dagger.json` contains `sdk:compat:develop` toolchain
3. Verifies `legacyRuntimeCodegen` is NOT present
4. Runs `dagger generate`
5. Verifies generated files exist (e.g., `dagger.gen.go`)
6. Verifies `.gitignore` does NOT contain generated file entries
7. Verifies `.gitattributes` DOES contain generated file entries
8. Runs `dagger call` to verify the module works with committed files

Follow the existing integration test patterns in `core/integration/module_test.go`.

- [ ] **Step 2: Write integration test for `dagger develop` deprecation**

Create a test that:
1. Runs `dagger init --sdk go` in a temp directory
2. Runs `dagger develop`
3. Verifies `legacyRuntimeCodegen: true` is set in `dagger.json`
4. Verifies generated files exist
5. Verifies the deprecation warning is printed

- [ ] **Step 3: Write integration test for migration path**

Create a test that:
1. Creates a module with `legacyRuntimeCodegen: true`
2. Installs `sdk:compat:develop` as toolchain
3. Runs `dagger generate`
4. Verifies `legacyRuntimeCodegen` is removed from `dagger.json`
5. Verifies `codegen.automaticGitignore: false` is set

- [ ] **Step 4: Run tests**

Run: `go test ./core/integration/ -run TestGenerate -v -count=1`

- [ ] **Step 5: Commit**

```
test: add integration tests for dagger generate flow
```

---

## Task Order and Dependencies

```
Task 1 (config field)
  ↓
Task 2 (expose on ModuleSource)
  ↓
Task 3 (sdk: prefix parsing) ──→ Task 4 (builtin ref resolution)
                                    ↓
Task 5 (compat module source) ──→ Task 6 (embed in engine)
                                    ↓
Task 7 (unhide generate) ─────→ Task 8 (init --sdk toolchain)
                                    ↓
Task 9 (deprecate develop) ────→ Task 10 (Go SDK optimization)
                                    ↓
                                 Task 11 (Python/TS optimization)
                                    ↓
                                 Task 12 (integration tests)
```

Tasks 1-2 are foundational. Tasks 3-6 (builtin ref plumbing) must complete before Task 8 (init --sdk), since `sdk:compat:develop` needs to resolve. Tasks 5-6 (compat module) and Tasks 7, 9 (CLI changes) can proceed in parallel after Tasks 1-2. Task 10 depends on Task 2. Tasks 11/11b are independent of each other. Task 12 ties everything together and must run last.
