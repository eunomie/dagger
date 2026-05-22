# Go SDK: skip codegen at runtime (Runtime phase only)

## Status

Proposed. Stacks on the existing `legacy-codegen-flag` stg patch, which
already introduces the `codegen.legacyCodegenAtRuntime` field on
`ModuleCodegenConfig`, parses it from `dagger.json`, and validates that
`false` is paired with `automaticGitignore=false`.

## Goal

When a Go module's `dagger.json` declares
`codegen.legacyCodegenAtRuntime: false`, the Go SDK's `Runtime()` stops
invoking `codegen generate-module` and instead trusts the committed
`dagger.gen.go` + `internal/dagger/dagger.gen.go` from the user's
context directory. `go build` then compiles the module straight from
disk, trimming the runtime hot path from
*codegen → go build → exec* to *go build → exec*.

## Scope

In scope:

- `Runtime()` branch in `core/sdk/go_sdk.go`.
- One sibling helper `baseWithoutCodegen` next to `baseWithCodegen`.
- A small `useRuntimeCodegen(source)` predicate.
- Pre-flight check that the two expected generated files exist, with a
  single uniform error message ("run `dagger develop` to (re)generate").

Out of scope (explicitly deferred):

- `Codegen()` — unchanged. `dagger develop` always regenerates.
- `ModuleTypes()` — unchanged. Type discovery still runs the codegen
  binary inside the runtime container, same as today.
- `workspaceModuleInit` / any auto-write of the flag in new
  `dagger.json` files. Users opt in by hand-editing until a later patch.
- `core/integration` tests. Will land in a follow-up patch.
- Other SDKs (Python, TypeScript, …). They read the field via the
  shared config struct but ignore it; their runtime flow is unchanged.

## Non-goals

- Detecting drift between committed `dagger.gen.go` and the live engine
  schema. If a user upgrades the engine without re-running `dagger
  develop`, the worst case is a `go build` failure that names the
  symbol; we trust the user to react.
- Sharing or refactoring the legacy `baseWithCodegen` path. The new
  helper is intentionally a sibling so neither side changes when the
  other does.

## Current state

`core/sdk/go_sdk.go:454` — `Runtime()` always calls
`sdk.baseWithCodegen(ctx, deps, source)` (line 472) before appending
`go build`, `withEntrypoint`, `withWorkdir`, and two `withoutMount`
calls.

`baseWithCodegen` (line 544) does, in order:

1. Fetch the schema introspection JSON (via the `deps` `SchemaBuilder`).
2. Build the base container with `sdk.base()` (Go toolchain + mod /
   build caches + `GOPROXY` + `GODEBUG` env).
3. `withoutFile(dagger.gen.go)` on the user's context dir.
4. Build `codegen generate-module …` argv.
5. Mount the schema JSON, mount the (stripped) context dir, set workdir.
6. Apply `goSDKConfig` selectors (currently injects `GOPRIVATE` if set).
7. Apply `gitConfigSelectors` (env-var / file plumbing for git auth).
8. Apply `getUnixSocketSelector` set/unset (SSH agent socket mount +
   `SSH_AUTH_SOCK` env, bracketed *around* the codegen exec).
9. Exec `codegen generate-module`.

So the SSH socket is present only during the codegen exec; by the time
`Runtime()` appends `go build`, SSH has been unset. That works in the
legacy path because codegen already populated the module cache via
`go mod tidy`. In the new path there is no codegen, so `go build` is
the first download trigger — SSH needs to stay live across it.

## Target state

```
dagger.json:
  codegen:
    automaticGitignore: false
    legacyCodegenAtRuntime: false

dagger call foo  (Go SDK, new mode):
  Runtime()
    ├─ useRuntimeCodegen(source) → false
    ├─ baseWithoutCodegen(source)
    │     ├─ requireGeneratedFiles → ok
    │     ├─ sdk.base() (Go toolchain + caches + GOPROXY/GODEBUG)
    │     ├─ mount context dir as-is at /src
    │     ├─ withWorkdir /src/<srcSubpath>
    │     ├─ getSDKConfigSelectors (GOPRIVATE if configured)
    │     ├─ gitConfigSelectors (git auth env)
    │     └─ setSSHAuthSelectors (socket + SSH_AUTH_SOCK)
    │           ↑ returns (ctr, unsetSSHSelectors, err)
    ├─ withExec(go build -ldflags=-s -w -o /runtime .)
    ├─ <unsetSSHSelectors> (no-op for legacy branch)
    ├─ withEntrypoint(/runtime)
    ├─ withWorkdir(RuntimeWorkdirPath)
    ├─ withoutMount(/go/pkg/mod)
    └─ withoutMount(/root/.cache/go-build)
```

No `codegen generate-module` exec. No `schema.json` mount. No
`withoutFile(dagger.gen.go)`. SSH is set across the entire `go build`
step and unset before the final container is returned.

## Design

### `useRuntimeCodegen` predicate

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

Package-level free function. The default-true matches the `omitempty`
JSON semantics of the field, so existing `dagger.json` files behave
exactly as before.

### `requireGeneratedFiles` precheck

```go
// requireGeneratedFiles verifies the module's committed generated files
// are present. Called only on the no-codegen path; the legacy path
// regenerates them so the check would be redundant.
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

Uses the existing `Directory.exists` field (verified in
`core/schema/directory.go:132` and used elsewhere at
`core/schema/directory.go:1210`).

One uniform error template covering both first-time opt-in and stale
post-modification cases. "(re)generate" reads naturally for both. The
message names the missing path and the single command that fixes it.

### `baseWithoutCodegen` helper

Returns an extra `[]dagql.Selector` containing the SSH unset selectors
so the caller can append them after `go build` runs.

```go
// baseWithoutCodegen prepares the runtime container when the module
// has opted out of runtime codegen.
//
// Returns the container plus a list of selectors that the caller must
// append after the `go build` exec (currently the SSH socket unset
// pair). Nil for the legacy branch (which never sets SSH this late).
func (sdk *goSDK) baseWithoutCodegen(
    ctx context.Context,
    src dagql.ObjectResult[*core.ModuleSource],
) (dagql.ObjectResult[*core.Container], []dagql.Selector, error) {
    var ctr dagql.ObjectResult[*core.Container]

    dag, err := sdk.root.Server.Server(ctx)
    if err != nil {
        return ctr, nil, fmt.Errorf("dag for go module sdk runtime: %w", err)
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
        return ctr, nil, fmt.Errorf("module context directory ID: %w", err)
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
        return ctr, nil, fmt.Errorf("prepare go module runtime container: %w", err)
    }
    return ctr, unsetSSH, nil
}
```

### `Runtime()` change

Single branch at the top, one extra append in the tail:

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

selectors := []dagql.Selector{
    {
        Field: "withExec",
        Args: []dagql.NamedInput{{
            Name: "args",
            Value: dagql.ArrayInput[dagql.String]{
                "go", "build",
                "-ldflags", "-s -w",
                "-o", goSDKRuntimePath,
                ".",
            },
        }},
    },
}
selectors = append(selectors, postBuildSelectors...) // unset SSH for the new path; nil for legacy
selectors = append(selectors,
    dagql.Selector{Field: "withEntrypoint", Args: …},
    dagql.Selector{Field: "withWorkdir", Args: …},
    dagql.Selector{Field: "withoutMount", Args: /* /go/pkg/mod */},
    dagql.Selector{Field: "withoutMount", Args: /* /root/.cache/go-build */},
)

if err := dag.Select(ctx, ctr, &ctr, selectors...); err != nil {
    return nil, fmt.Errorf("failed to build go runtime binary: %w", err)
}
```

The legacy branch's behavior is bit-identical: `postBuildSelectors` is
nil, the append is a no-op, and the rest of `Runtime()` is unchanged.

## Error handling

Three error sites in the new path:

1. `requireGeneratedFiles` returns the actionable message described
   above. Short, names the missing file, names the fix.
2. Config-decode failures (`mapstructure`) bubble up unchanged from the
   shared logic with `baseWithCodegen` — same wording, same conditions.
3. The final `dag.Select` failure is wrapped as `"prepare go module
   runtime container: %w"`. Matches the style of the legacy helper's
   final wrap (`"failed to mount introspection json file into go module
   sdk container codegen"`) without lying about which step failed.

A `go build` failure (e.g., schema drift after a non-local engine
upgrade) surfaces at exec time below this layer — the user sees the
compiler's symbol-not-found error. Out of scope to wrap that; the file
precheck is the only proactive guard.

## Testing

This patch adds no integration tests; that lands in a follow-up patch
the user explicitly scoped out. Unit-test coverage from the
`legacy-codegen-flag` patch already exercises the config-side
invariants (`TestModuleCodegenConfig_Validate`,
`TestModuleCodegenConfig_Clone` in `core/modules/config_test.go`).

Manual verification done by the author before refresh:

- `go build ./core/sdk/...` clean.
- `go vet ./core/sdk/...` clean.
- Existing `dagger.json` files (no `codegen.legacyCodegenAtRuntime`
  field) trigger `useRuntimeCodegen → true`, hit `baseWithCodegen`
  unchanged → byte-identical legacy behavior.

## Rollout

Single stg patch on top of `legacy-codegen-flag`, message:

```
core/sdk/go_sdk: skip codegen at runtime when opted in

When dagger.json has codegen.legacyCodegenAtRuntime=false, Go SDK's
Runtime() skips `codegen generate-module` and trusts the committed
dagger.gen.go + internal/dagger/dagger.gen.go from the user's
context directory. `go build` compiles the module straight from disk.

Codegen still runs in Codegen() (dagger develop) and ModuleTypes().
Other SDKs ignore the flag for now.

A small precheck verifies the expected generated files exist before
building; missing-file errors point users at `dagger develop` to
(re)generate.

Signed-off-by: Yves Brissaud <yves@dagger.io>
```

## Risks & open items

| Risk | Likelihood | Mitigation |
|---|---|---|
| Schema drift between committed `dagger.gen.go` and live engine schema | Medium for users upgrading the engine without `dagger develop` | `go build` fails with a symbol-not-found error that names the missing identifier; the user re-runs `dagger develop`. |
| Private-module fetch failure during `go build` in the new path | Low — gitconfig + SSH parity replicated | If a user reports it, compare to legacy behavior (which also depends on the same selectors during codegen's `go mod tidy`). |
| `Directory.exists` semantics differ from `os.Stat` for the engine-mounted context dir | Very low — same call used in `core/schema/directory.go:1210` for a similar precheck | None needed; if it surfaces, fix the helper. |

No new public API. No `dagger.json` schema change (the flag already
exists). No CLI surface change. No telemetry change beyond fewer
spans on the new path.
