# Global Module Settings

Configure a module in `dagger.toml` without installing it: a `modules`
entry keyed by source ref, with no `source` field, applies its settings
to every load of that source — at any depth of any dependency tree. The
`modules` table stays the single place where modules are described;
whether an entry has a `source` decides whether it *loads* something or
only *configures* it.

## Table of Contents
- [Problem](#problem)
- [Solution](#solution)
- [Core Concept](#core-concept)
  - [Motivating examples](#motivating-examples)
  - [Matching](#matching)
  - [Injection](#injection)
  - [Typed values](#typed-values)
  - [Engine mechanics](#engine-mechanics)
  - [Security model](#security-model)
- [CLI](#cli)
- [Status](#status)

## Problem

1. **Settings only reach installed modules** - `[modules.<name>.settings]`
   injects constructor defaults into the workspace's own instance (and
   toolchains); a module loaded as a dependency receives nothing
   (`core/schema/modulesource.go:3325`).
2. **Transitive modules are invisible** - if module A depends on module B,
   and B builds with a toolchain module, nothing in A's workspace can
   configure that toolchain — it never appears in `dagger.toml`.
3. **Configuring must not mean installing** - the `modules` table is the
   workspace's load manifest; today the only way to attach settings to a
   module is an installed entry, which loads the module into the
   workspace as a side effect.
4. **Shared modules want one configuration** - toolchains
   (`github.com/dagger/dagger/modules/go`, `github.com/dagger/java`),
   base-image providers (`modules/alpine`, `modules/wolfi`), or modules
   needing credentials (`modules/dev`'s `githubToken`) are consumed by
   SDK runtimes and other modules; their settings — versions, mirrors,
   registries, proxies, tokens — are properties of the
   *workspace/environment*, not of each consumer.

## Solution

Allow configuration-only entries in the `modules` table: an entry keyed
by a module source ref, carrying **no `source` field** and only
`settings`. Such an entry loads nothing. Whenever the engine loads *any*
module in the session — root module, dependency at any depth, toolchain,
SDK runtime module — whose canonical resolved source matches the entry's
key, the settings (with the active `[env.*]` overlay applied) are
injected as constructor argument defaults. The presence of `source` is
what separates the two kinds of entry: with it, the module is installed
and the entry's settings scope to that instance (unchanged behavior);
without it, the key is the source ref and the settings apply globally.

## Core Concept

```toml
# Installed module: has a source, loaded into the workspace, callable.
[modules.my-app]
source = "./my-app"
entrypoint = true

# Configuration-only: no source, the key is the source ref.
# Loads nothing; applies to any load of this source in the session.
[modules."github.com/dagger/dagger/modules/go".settings]
version = "1.25"
base = "registry.corp.example/golang:1.25"

[modules."github.com/dagger/dagger/modules/alpine".settings]
extraRepositories = ["https://mirror.corp.example/alpine"]
extraKeyUrls = ["https://mirror.corp.example/keys/release.pub"]
```

Neither module above is installed or loaded by the workspace itself.
Their settings apply whenever the matching source is constructed
anywhere in the session — by an installed module, by a
dependency-of-a-dependency, or by an SDK runtime during
`dagger develop`.

### Motivating examples

Real constructor arguments from modules in this repository (and the
java toolchain from [Java Private
Registries](java-private-registries.md)) that are environment-shaped —
values a platform team wants to set once, not in every consumer:

| Module | Args | Configured once, you get |
|---|---|---|
| `github.com/dagger/dagger/modules/go` (`toolchains/go/main.go:28`) | `version`, `base: Container`, `extraPackages`, `moduleCache` | one Go version policy across every module built with the toolchain; a corporate hardened base image (internal CAs, proxies) substituted everywhere, including transitively |
| `modules/alpine` (`modules/alpine/main.go:29`) | `extraRepositories`, `extraKeyUrls`, `branch` | APK mirrors for air-gapped or corporate networks. `wolfi.Container()` forwards its optional args to the `alpine` constructor, omitting unset ones — so a setting on `alpine` reaches containers built through `wolfi` too |
| `github.com/dagger/java` | `mavenSettings: Secret`, `registryUrl`, ... | private Maven registries for codegen and runtime — the deep-dive in [Java Private Registries](java-private-registries.md) |

The same shape keeps recurring: a private registry/mirror, a credential,
a version or base-image policy. Today each of these is either
copy-pasted into every consumer's constructor call, hardcoded, or simply
impossible to reach when the module is transitive. A natural follow-up
in the same shape: the Go toolchain growing `goproxy`/`netrc: Secret`
args for private Go modules, configured globally exactly like the Java
registries.

### Matching

- A configuration-only entry's **key** must parse as a module source ref
  (git ref or local path); a bare name without `source` is a config
  error. Fields other than `settings` (`entrypoint`, `up`, ...) are
  rejected on configuration-only entries.
- The key is matched against the **canonical resolved source** of every
  loaded module: kind + clone ref + source root subpath
  (`ModuleSource.AsString()`, `core/modulesource.go:827`). A module
  cannot claim a ref by name; it must actually resolve from there.
- Matching is **version-agnostic** by default: an entry for
  `github.com/dagger/dagger/modules/go` matches any version or pin of
  that repo and subpath. A version in the key
  (`"github.com/dagger/dagger/modules/go@v0.19"`) restricts the match.
- Local sources match by context directory + subpath
  (`[modules."./vendored/go-toolchain".settings]`).
- Configuration-only entries never trigger a load; matching happens when
  something else loads the source.

### Injection

The installed instance of a source is itself a load of that source, so
global settings reach it too; its own instance settings win:

| Priority | Source |
|---|---|
| 1 | explicit argument from the caller (module code or CLI flag) |
| 2 | installed entry settings, scoped to the workspace instance (today's behavior) |
| 3 | matched configuration-only settings (with env overlay) |
| 4 | schema defaults from the module's constructor |

Only constructors are affected, like today (`core/modfunc.go:565`).

### Typed values

Unchanged — this design adds no typing mechanism because one exists.
Settings values are TOML scalars for primitive args; for object-typed
args (Secret, File, Directory, Service, ...) the string value is an
*address* resolved lazily at constructor call time via `Query.address`
(`core/schema/address.go`) in the **main client's session**
(`core/modfunc.go:450`):

| Arg type | Example values |
|---|---|
| `String`, `Int`, `Bool`, lists | `"us-east-1"`, `7`, `true`, `["a", "b"]` |
| `Secret` | `env://TOKEN`, `file://~/.m2/settings.xml`, `op://vault/item` |
| `File` / `Directory` | `~/.config/foo.yaml` (host, via filesync), git URLs |
| `Container` | `registry.corp.example/golang:1.25` |
| `Service` | `tcp://localhost:5432` |

Host files and secrets are fetched through the caller's session
attachables (filesync, secret provider), so the same key resolves to a
host file locally and a CI secret under `--env=ci`.

### Engine mechanics

- The workspace's `(source -> settings)` map is threaded into every
  `asModule` call as an internal argument — the pattern used today by
  `LegacyWorkspaceConfigJSON` (`core/schema/modulesource.go:3199`) but
  passed down through dependency loading
  (`loadDependencyModules`, `core/schema/modulesource.go:3306`) and the
  SDK loader (`core/sdk/loader.go`).
- Passing it as a call argument keeps cache identity correct: sessions
  with different settings produce different call IDs; the existing
  variant digest is the salt (`AsModuleVariantDigest`,
  `core/schema/modulesource.go:3181`).
- On match, the settings map becomes `Module.WorkspaceConfig`; the
  existing `UserDefault` resolution (`core/modfunc.go:565`) and
  `--help` integration (`ApplyWorkspaceDefaultsToTypeDefs`,
  `core/module.go:257`) consume it unchanged.
- Only addresses (strings) enter module identity; resolved secret and
  file contents stay out, as today.

### Security model

- Settings attach to the resolved source, pin included — not to a name a
  module can spoof.
- A settings entry is a trust statement with global blast radius: any
  module in any tree run from this workspace that depends on the matched
  source receives the values, including secrets. That is the feature,
  and its cost; it is no broader than what the user wrote in
  `dagger.toml`.
- Secret values resolve lazily, only when a matched constructor actually
  runs; traces and `--help` show the address (`env://GITHUB_TOKEN`,
  `op://infra/github/token`), never the value.
- The nested-client guard is intact: module *code* still cannot reach
  host files, secrets, or git credentials. Only declarative,
  user-authored workspace config routes host resources to a module, as
  with installed-module settings today.

## CLI

Configure modules nobody installed — `my-app` depends on module `B`;
`B` builds its artifacts with the Go toolchain and its containers on
alpine/wolfi. Neither toolchain appears anywhere in the workspace:

```bash
$ cat dagger.toml
[modules.my-app]
source = "./my-app"
entrypoint = true

[modules."github.com/dagger/dagger/modules/go".settings]
version = "1.25"
base = "registry.corp.example/golang:1.25"

[modules."github.com/dagger/dagger/modules/alpine".settings]
extraRepositories = ["https://mirror.corp.example/alpine"]

$ dagger call build   # B's toolchain uses Go 1.25, the corporate base
                      # image, and the internal APK mirror
```

Secrets work the same — one GitHub token for every module that takes
one, from the local keychain in dev and from the environment in CI:

```toml
[modules."github.com/dagger/dagger/modules/dev".settings]
githubToken = "op://infra/github/token"

[env.ci.modules."github.com/dagger/dagger/modules/dev".settings]
githubToken = "env://GITHUB_TOKEN"
```

```bash
$ dagger --env=ci -m github.com/dagger/dagger/modules/dev call github --args=pr,list
```

## Status

Proposal. Decision points: version-restricted matching syntax, the exact
spelling of configuration-only entries (`[modules."<ref>"]` without
`source`, vs a separate singular `[module."<ref>"]` section), and whether
a user-global config (`~/.config/dagger/dagger.toml`) merges under the
workspace config for machine-wide setup.

---

- Next: [Java Private Registries](java-private-registries.md)
