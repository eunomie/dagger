# Handover — Go SDK "no codegen at runtime" + single-pass codegen (+ follow-ups)

_Last updated: 2026-05-28_

## TL;DR

- **Branch:** `workspace-go-no-codegen-at-runtime` (StGit stack), based on the
  upstream `workspace` branch.
- **Remote:** `origin` = `git@github.com:eunomie/dagger.git`. Local `HEAD`
  **== remote head** (`cc4018949`) — the whole applied stack is pushed and in
  sync.
- **PR:** https://github.com/dagger/dagger/pull/13210 — OPEN, **base
  `workspace`** (not `main`), head `eunomie:workspace-go-no-codegen-at-runtime`.
  Body is up to date (describes all patches + the known workspace-load issue
  which is now fixed by the `ws-generate-narrow-*` patches).
- **Working tree:** clean (ignore `.dagger/lock` and `.claude/scheduled_tasks.lock`).
- **Only local-only thing:** the popped/unapplied `go-sdk-no-codegen-design-docs`
  patch (6 design docs under `hack/designs/2026-05-2x-*.md`). It is NOT on the
  remote — see "Transferring to another machine".

## Resuming on another machine

1. Clone the dagger repo and add the fork remote:
   `git remote add origin git@github.com:eunomie/dagger.git`
2. `git fetch origin workspace-go-no-codegen-at-runtime` then check it out.
3. Initialize StGit on the branch: `stg init` then `stg uncommit -t <merge-base-with-workspace>`
   if you want the commits as patches, OR just work with the branch as-is.
   (On this machine the patches already exist in `.git/patches`; that metadata
   does NOT travel via the branch ref — only the commits do. Re-deriving the
   stack with `stg uncommit` reconstructs equivalent patches.)
4. Tooling expected:
   - **StGit** (`stg`, v2.5.x Rust build). User workflow: stacked patches, never
     plain `git commit`. Every patch message ends with
     `Signed-off-by: Yves Brissaud <yves@dagger.io>`, never `Co-Authored-By`.
   - **`dagger-main`** CLI (the user's released-engine binary, `~/.local/bin/dagger-main`)
     used for `dagger-main generate -y ...`.
   - **Dev engine** via `./hack/dev <cmd>` (builds engine from source + runs cmd
     against `dagger-engine.dev`) and `./hack/with-dev <cmd>` (runs against an
     already-built dev engine; no rebuild).
   - Claude skills used: `cache-expert` (trace replay/debugging), `stg`,
     `organize-patches`, `superpowers:*`.
5. `apply.whitespace=fix` is set **globally** in the user's git config — see
   "Gotchas".

## The patch stack (bottom → top)

All applied and pushed. SHAs are this-machine values; they change on rebase.

| # | Patch | SHA | What it does |
|---|-------|-----|--------------|
| 1 | `go-sdk-legacy-codegen-flag` | 87feb0fab | Adds `codegen.legacyCodegenAtRuntime` config field + `Validate()` (false requires `automaticGitignore=false`). |
| 2 | `go-sdk-schema-tools` | b73052677 | Engine `schemaTools` (`dag.schema(json)` + Schema object) + regenerated SDK clients/docs. (Later trimmed by #8.) |
| 3 | `go-sdk-runtime-skip-codegen` | 208743d8c | Go SDK skips runtime codegen when `legacyCodegenAtRuntime=false`; trusts committed files; helpful missing-files error. Also updates `TestTelemetry` broken-module goldens (errors now surface at `asModule`/build time). |
| 4 | `go-sdk-empty-parentname-dispatch` | f5ef01830 | Generated `invoke()` empty-parentName arm so self-types are discoverable without `moduleTypes`. |
| 5 | `go-sdk-single-pass-codegen` | be03592c4 | Single-pass Go codegen for self-calls: discover own types via `packages.Load`, emit introspection JSON, merge via `schema.merge`. Drops `moduleTypes` from Go SDK. Contains `cmd/codegen/.../introspect_emit.go` (the emitter). |
| 6 | `sdk-codegen-introspection-types` | f03bb0e94 | Rust (`r#type` keyword) + Elixir (nullable-object-list) codegen fixes + regenerated rust/elixir bindings. **Now mostly dormant** after #8 removed the IntrospectionType graph (see Open items). |
| 7 | `e2e-runtime-codegen-tests` | ca5549c9c | `core/integration/module_runtime_codegen_test.go`: validation guardrail + missing-generated-files error for `legacyCodegenAtRuntime`. |
| 8 | `schematools-merge-only` | 1107213ea | Trims schemaTools to `schema`/`merge`/`contents`; drops `listTypes`/`hasType`/`describeType` + the whole `IntrospectionType` object graph; regenerates all SDKs (−~22k lines, deletes 42 `Introspection*` files); rewrites schematool tests to assert via `contents`. |
| 9 | `ws-generate-narrow-engine` | 513d4c009 | Engine: `WorkspaceModulesInclude` client-metadata hint → narrows pending workspace modules by include patterns before load (segment before `:`, or a bare token matching a module name). Unit test `TestFilterPendingWorkspaceModulesByInclude`. |
| 10 | `ws-generate-narrow-cli` | 78bd9aad5 | `dagger generate <module[:gen]>` passes its args as `WorkspaceModulesInclude`, so a broken/stale unrelated module no longer blocks generation. |
| 11 | `go-fix-go-self-call-schema` | 8b2c6bd47 | Colleague's squashed PR #2 (eunomie/dagger#2): emitter exposes struct **fields** in lowerCamel (`Message`→`message`) + self-calls dependency/transitive test fixtures. Absorbed via `stg repair`. Keeps colleague authorship (Guillaume de Rouville). |
| 12 | `go-self-call-schema-casing` | cc4018949 | **(top)** Follow-up to #11: emitter now matches engine name casing everywhere — enum values→`ToScreamingSnake`, args→`ToLowerCamel`, type names + type refs→`ToCamel`. Adds a `Color` enum to the `go/self-calls` fixture + a `DescribeSelf` self-call test (`{"describeSelf":"got green"}`). |

Popped / unapplied (local-only): `go-sdk-no-codegen-design-docs` (the 6 design
docs). Plus ~20 `archive-*` patches preserved from an earlier `organize-patches`
run (safe to delete eventually).

## The core casing principle (important for future emitter work)

`cmd/codegen/generator/go/templates/introspect_emit.go` produces the **final**
introspection schema — i.e. it must reproduce exactly what the **engine**
exposes from the same Go source. The TypeDef codegen path passes raw Go names
and lets the engine normalize them (`core/typedef.go`); the emitter has to do
that normalization itself:

- type names (object/interface/enum) + all type refs → `strcase.ToCamel`
- field / function / argument names → `strcase.ToLowerCamel`
- enum value names → `strcase.ToScreamingSnake`
- scalar names and `Query` are literals.

If you add any new `Name:` emission in that file, apply the matching normalizer.

## Validation / common commands

- Regenerate everything: `dagger-main generate -y` (≈6–12 min; rebuilds engine
  from source). Targeted: `dagger-main generate -y php-sdk:api docs:references`,
  `... rust-sdk:apiclient`, etc.
- Run integration tests against a freshly built engine (containerized, no host
  writes): `dagger-main call engine-dev test --pkg ./core/integration --run '^TestModule/TestSelfCalls' --test-verbose --count=1 --timeout=20m`
  (TestSelfCalls currently: **18 passed**).
- Host go test vs a running dev engine: `./hack/dev go test ./... -run ...`
  (rebuilds engine) or `./hack/with-dev go test ...` (no rebuild).
- Unit tests (no engine): `go test ./core/ ./engine/server/ ./cmd/codegen/... -run ...`.
- CI traces (cache-expert skill): `dagger --progress=plain trace <id> > /tmp/t.log 2>&1`.
  Map PR checks → trace IDs via the Dagger Cloud GraphQL API (see the
  cache-expert `references/debugging.md`).

## Gotchas (learned the hard way)

- **`apply.whitespace=fix` strips trailing whitespace.** doctum-generated php
  HTML (`docs/static/reference/php/...`) legitimately has trailing whitespace.
  `git add` preserves it, but `stg refresh -p <deep-patch>` (which pops/pushes
  patches) re-strips it → the `*:generated` / `php-sdk:api` CI checks then fail.
  Fix: fold regenerated php docs into the **top** patch (top refresh doesn't
  pop/push), or fold while overriding whitespace handling. Verify with
  `git show <sha>:docs/static/reference/php/Dagger/Env.html | grep -c ' $'`.
- **Local dev engine deadlocks on the broken-module path.** `TestTelemetry`
  golden subtests and `dagger generate`/`dagger call` on intentionally-broken
  modules hang in *this* machine's engine (the known workspace-load issue).
  CI does NOT hit this. So `-update` for those goldens can't be run locally;
  they were hand-edited from CI trace output.
- **CI flakes seen (not code bugs):** "engine is shutting down" (one CI engine
  instance dies, cascading to several checks); `golang:check` k3s e2e infra;
  `test-split:test-container` `.dagger/lock` filesync race. Re-run clears them.
- **`dagger generate`'s engine version vs branch `engineVersion`.** `dagger-main`
  is a newer release than the branch's `engineVersion` (v0.20.6); it bumps the
  `dagger.io/dagger` pin in test-module go.mod/go.sum across ~138 files. That
  churn is NOT part of any fix — revert it after regen
  (`git checkout -- $(git diff --name-only -- '**/go.mod' '**/go.sum')`).
- **Stray `dagql/idtui/.git`.** Golden-test runs can leave a stray nested git
  repo there which breaks `dagger generate`'s final `git add -A`. Remove with
  `rm -rf dagql/idtui/.git` if regen fails with "does not have a commit checked out".

## Open items / next steps

1. **broken-module e2e for the generate-narrowing fix** (`ws-generate-narrow-*`):
   not yet run. Set up a workspace with one intentionally-broken toolchain and
   confirm `dagger generate <good-module>` still works (skips the broken one).
   Unit test + happy-path smoke (`dagger generate -l go-sdk`) are green; the
   span-level "fewer modules loaded" proof wasn't captured locally.
2. **Dormant rust/elixir codegen fixes (patch #6).** After #8 removed the
   `IntrospectionType` graph, the `r#type` (rust) and `unwrap_list` (elixir)
   fixes are no longer exercised. They're harmless general robustness — decide
   whether to drop patch #6 to slim the series.
3. **`schematools-merge-only` review.** It's a dedicated "trim" patch on top of
   the original schemaTools patch (#2) for reviewability; consider squashing
   #2+#8 once reviewed.
4. **Design docs / this handover are local-only** — see below.

## Transferring the local-only docs to another machine

The applied stack is fully pushed, so `git fetch` brings all 12 patches'
content. NOT on the remote: the popped `go-sdk-no-codegen-design-docs` patch and
this handover file (kept off the PR branch on purpose). To carry them over,
either:

- push them to a scratch branch on the fork (does not touch the PR branch):
  `git push origin <commit>:refs/heads/wip/handover-and-design-docs`, or
- `stg export` the patches and copy the resulting files, or
- just copy `hack/designs/*.md` directly.
