# Java SDK external-module work — handover

_Last updated: 2026-05-28 — Yves Brissaud_

Continuation notes for the "Java SDK as an external UX module" work, so it can be
picked up on a fresh machine. The companion design doc lives in the java-sdk repo
at `.claude/docs/java-sdk-migration.md`; this file is the cross-repo map + the
current blocker.

## TL;DR

The Java SDK is split into two layers:

1. **Runtime — stays builtin in this repo** (`sdk/java`): codegen, the Maven
   build, module execution. User modules keep `sdk.source: "java"` and are run by
   the engine's builtin runtime.
2. **User-experience helper — external** at `github.com/dagger/java-sdk`: a Dang +
   `polyfill` module providing `init` / `generate` / `migrate` / deps / engine
   helpers. It does **not** contain the runtime.

An earlier self-contained approach (vendoring the runtime + Maven into java-sdk,
`sdk.source: github.com/dagger/java-sdk/runtime`) was **reverted** in favor of
this split.

## Repos, branches, remotes (clone these on the new machine)

Use the standard `…/src/github.com/dagger/<repo>` layout.

| Repo | Branch | Where it's pushed | Head |
|------|--------|-------------------|------|
| `github.com/dagger/dagger` (this repo, the engine) | `new-java-sdk` | `eunomie/dagger` (origin) | `e1c225929` |
| `github.com/dagger/java-sdk` (UX helper) | `main` | `eunomie/java-sdk` (origin) | `2c668bb` |
| `github.com/dagger/sdk-sdk` (polyfill dep) | see blocker below | fork `eunomie/sdk-sdk`, upstream `dagger/sdk-sdk` | — |

Both feature branches are on the user's forks (`eunomie/*`), not upstream
`dagger/*`. The engine `new-java-sdk` branch is a large shared workspace branch;
the Java work is the single patch `engine-stop-java-scaffold` on top.

## What changed

### This repo — `sdk/java/runtime` (patch `engine-stop-java-scaffold`, `e1c225929`)

"Stop scaffolding in the runtime." The runtime now only does codegen + Maven build
+ pom version-sync; scaffolding is owned by the helper's `init`.

- Removed `addTemplate` (+ its `replace`/`repl` helpers, the `strcase` import) and
  the scaffolding call in `codegenBase`.
- Removed `sdk/java/runtime/template/`.
- `codegenBase` now expects the user module to already contain a `pom.xml`.
- `sdk/java/README.md`: "create a new module" → `dagger call java-sdk init`.
- **Behavior change:** bare `dagger module init --sdk=java` no longer scaffolds a
  module (the engine delegates scaffolding to the runtime, which no longer does
  it). Supported entry point is now `dagger call java-sdk init`.

### `github.com/dagger/java-sdk` (patch `java-sdk-ux-helper`, `2c668bb`)

- `init` emits `sdk.source: "java"`; discovery regex matches `"source": "java"`.
- Added `migrate` / `migrate-all` / `migrate-pom` (path-based on `JavaSdk`):
  re-merge `pom.xml` from the template (preserve groupId/artifactId/name +
  user-added deps, refresh build config + `dagger.module.deps`), drop `target/`,
  regenerate.
- Added `helpers/migrate-pom` (Go) implementing the pom re-merge.
- Removed the vendored `runtime/` (Go runtime + Maven projects).
- e2e: fixtures use `sdk.source: "java"`; added a `migrate/app` fixture +
  `migratePomCheck`.
- Dang note: a `pub` function cannot return an external dependency type
  (`PolyfillWorkspaceFork`); fork builders are kept as private `let` fields.

## ⚠️ Current blocker — polyfill ↔ engine version gap

Workspace functions (`init`, `generate`, `migrate`, and the e2e `@check`s) require
a `polyfill` build matching the running engine, because the engine reconstructs
the `Workspace` argument inside the polyfill module via `loadWorkspaceFromID`.

Last session ran the **released** CLI/engine `v0.21.0`
(`registry.dagger.io/engine:v0.21.0`). On it, every workspace function fails with:

```
convert arg ws: module "module {Polyfill…}" does not have a field "loadWorkspaceFromID"
```

This reproduces on the **unmodified** `java-sdk:generate-all`, so it is a
polyfill/engine-version mismatch, not a defect in the helper. Engine v0.21.0 sits
in a gap:

- canonical `dagger/sdk-sdk/polyfill@main` (`2f1d2ab`) targets ≤ v0.20.x;
- branch `dagger/sdk-sdk` `codex/migrate-1.0` (`df33620`, "migrate to Dagger 1.0")
  targets Dagger 1.0;

and **neither resolves `loadWorkspaceFromID` on v0.21.0** (verified by repinning to
`codex/migrate-1.0` and re-running). The committed java-sdk pin is canonical
`2f1d2ab`.

To unblock, pick one and align versions:

- pin the polyfill to whatever `dagger/sdk-sdk` revision matches the engine you run, **or**
- run a **dev engine built from this `new-java-sdk` branch** (see the
  `engine-dev-testing` skill) and pin the polyfill that matches it.

Note: the released v0.21.0 engine has its own builtin `sdk/java`, so the runtime
changes on this branch are only exercised by a dev engine built from it.

## Verification status

- ✅ `github.com/dagger/java-sdk` type-checks; `dagger functions` lists `init`,
  `generate-all`, `migrate`, `migrate-all`, `migrate-pom`, `mod`, `modules`, …
- ✅ `helpers/migrate-pom` unit-tested locally: well-formed XML, identity +
  user-added deps preserved, template build config restored, stale
  `dagger.module.deps` refreshed, annotation processor not corrupted.
- ✅ Engine runtime compiles after the de-scaffold (`go build`, `go vet`,
  `go mod tidy`).
- ❌ End-to-end `init`/`generate`/`migrate` + e2e checks — blocked by the polyfill
  gap above.

### Unit-testing migrate-pom without an engine

```sh
cd <java-sdk>/helpers/migrate-pom
go build -o /tmp/migrate-pom .
/tmp/migrate-pom <module-path-hint> <old-pom.xml> <java-sdk>/templates/default/pom.xml /tmp/out-pom.xml
```

## Next steps / TODO

1. Resolve the polyfill↔engine gap (pin a matching polyfill or run a dev engine
   from this branch), then run `dagger check` / `dagger call java-sdk init` /
   `migrate-pom` to get e2e green.
2. Decide whether bare `dagger module init --sdk=java` must keep working; if so,
   restore a minimal scaffolding fallback in the runtime.
3. When ready, open PRs to upstream `dagger/dagger` (`new-java-sdk` → engine) and
   publish `dagger/java-sdk` (currently only on the `eunomie` forks).
4. Java-side SDK improvements (explicitly deferred from this migration).

## Cheat sheet

```sh
# helper module
cd <java-sdk>
dagger functions
dagger call java-sdk init --name=demo
dagger call java-sdk migrate-pom --path=path/to/module
dagger check                      # runs workspace checks (blocked until polyfill aligned)

# engine dev build (to exercise sdk/java runtime changes) — see engine-dev-testing skill
```
