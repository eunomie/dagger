# Java Private Registries

*Builds on [Global Module Settings](global-module-settings.md)*

Private registry access for Java modules through a central
`github.com/dagger/java` toolchain module that owns all Maven/JRE
execution. Configured once — globally via module settings — it covers
both codegen (`dagger develop`) and module runtime.

## Table of Contents
- [Problem](#problem)
- [Solution](#solution)
- [Core Concept](#core-concept)
  - [The java module](#the-java-module)
  - [Registry authentication options](#registry-authentication-options)
  - [Synthesized settings.xml](#synthesized-settingsxml)
  - [Java SDK runtime](#java-sdk-runtime)
- [CLI](#cli)
- [Status](#status)

## Problem

1. **No registry configuration in the Java SDK** - Every Maven invocation
   (codegen during `dagger develop`, build/package at module runtime)
   only reaches Maven Central; there is no way to provide a
   `settings.xml`, mirror, or credentials (`sdk/java/runtime/main.go`).
2. **Java execution is scattered** - The SDK runtime builds its own
   Maven/JRE containers (`sdk/java/runtime/images/`); there is no central
   place a registry setting could live.
3. **Credentials are environment-shaped** - Locally they live in
   `~/.m2/settings.xml`; in CI they are secrets. The configuration
   mechanism must span both without changing module code.

Prior art in other SDKs is repo-local and untyped: TypeScript picks up a
committed `.npmrc`, Python exposes index URLs. Neither covers host
credentials or per-environment variation.

## Solution

A central `github.com/dagger/java` module provides the Maven and JRE
containers used by the Java SDK runtime and any Java-adjacent module.
Its constructor accepts registry configuration in two forms: structured
per-registry credentials (from which it synthesizes a `settings.xml`
that contains no secret material), or a complete `settings.xml` passed
as a `Secret` for full compatibility. [Global module
settings](global-module-settings.md) deliver that configuration to every
occurrence of the module, even when it is only a transitive dependency.

## Core Concept

### The java module

```graphql
"""
Central Java toolchain: Maven and JRE containers with shared configuration.
"""
type Java {
  """Maven container with registry configuration applied."""
  maven: Container!

  """JRE container for running packaged applications."""
  jre: Container!

  """
  Maven invocation helper: maven container with the settings file
  mounted and `-s` wired, ready to run the given goals.
  """
  mvn(args: [String!]!): Container!
}
```

Constructor — the surface that global module settings target:

```graphql
java(
  """
  Complete settings.xml applied to every Maven invocation.
  Accepts any secret address: file://~/.m2/settings.xml, env://MAVEN_SETTINGS, op://...
  Takes precedence over the structured registry arguments.
  """
  mavenSettings: Secret,

  """Private registry URL (resolves releases and snapshots)."""
  registryUrl: String,

  """Username for the private registry."""
  registryUsername: String,

  """Password or token for the private registry."""
  registryPassword: Secret,

  """Mirror URL for Maven Central (no authentication)."""
  mavenMirror: String,

  """JDK version for the maven and jre containers."""
  jdkVersion: String! = "21",
): Java!
```

### Registry authentication options

Investigation result: **every Maven credential path terminates in
`settings.xml` (or a resolver extension)** — Maven has no env-only or
CLI-only credential mechanism. What can differ is who authors the file
and where the secret material lives:

| Option | How | Pros | Cons | Verdict |
|---|---|---|---|---|
| Share the host/CI `settings.xml` | `mavenSettings: Secret`, address `file://~/.m2/settings.xml` or `env://...` | Maximal compatibility: multiple servers, mirrors, proxies, HTTP-header auth (GitLab job tokens), `settings-security.xml` | Opaque blob; may carry unrelated config; whole file is secret | **Supported** — the escape hatch |
| Structured args → synthesized `settings.xml` | `registryUrl` + `registryUsername` + `registryPassword`; generated file uses `${env.VAR}` interpolation, secret injected via `withSecretVariable` | Typed, granular secrets; no host file needed in CI; generated file contains no secret material | Covers the common single-registry case, not exotic setups | **Supported** — the default path |
| Committed `settings.xml` template in the user repo + env secrets | the `.npmrc` pattern: file in repo, `${env.TOKEN}` placeholders | No engine or toolchain feature needed | Per-repo duplication; consumers of a published module can't change it; doesn't reach transitive modules | Not chosen; still works manually |
| Mirror / pull-through proxy as a Dagger `Service` | `mavenMirror: Service` bound into the build container | Credentials never enter the build container at all | Requires running a registry proxy; heavier infra | Future extension |
| Credential-helper resolver extensions | cloud wagons (GCP `artifactregistry-maven-wagon`, AWS CodeArtifact) declared in the user's `pom.xml` | Short-lived cloud-native tokens | Cloud-specific, configured per project | Composes with both supported forms (the module passes env secrets through) |

### Synthesized settings.xml

With structured args, the module generates:

```xml
<settings>
  <servers>
    <server>
      <id>dagger-registry</id>
      <username>${env.DAGGER_MAVEN_REGISTRY_USERNAME}</username>
      <password>${env.DAGGER_MAVEN_REGISTRY_PASSWORD}</password>
    </server>
  </servers>
  <profiles>
    <profile>
      <id>dagger-registry</id>
      <repositories>
        <repository>
          <id>dagger-registry</id>
          <url><!-- registryUrl --></url>
        </repository>
      </repositories>
    </profile>
  </profiles>
  <activeProfiles>
    <activeProfile>dagger-registry</activeProfile>
  </activeProfiles>
</settings>
```

`${env.VAR}` interpolation in `<server>` blocks is standard Maven
([settings reference](https://maven.apache.org/settings.html)). The
secret flows as a container env secret (`withSecretVariable`), so neither
the generated file, the layer cache, nor the shared `.m2` cache volume
ever holds credential material.

### Java SDK runtime

`sdk/java/runtime` (Go module) adds `github.com/dagger/java` as a module
dependency and replaces its private `mvnContainer()` / `jreContainer()`
(`sdk/java/runtime/main.go`) with `dag.Java().Maven()` / `.Jre()`:

```go
// before: ctr, err := m.mvnContainer(ctx); ... WithExec(m.mavenCommand("mvn", ...))
ctr := dag.Java().Mvn([]string{"clean", "install", "-DskipTests"})
```

Because the runtime's `dag.Java()` constructor call is dispatched by the
engine, global module settings apply to it like to any other consumer,
and both phases are covered by construction:

- **Codegen** (`dagger develop`): the runtime's `Codegen` function builds
  the SDK and user module with the configured Maven container.
- **Runtime** (`ModuleRuntime`): build and package of the user module
  pull dependencies through the same configuration.

The settings file (shared or synthesized) is mounted outside the shared
`/root/.m2` cache volume (e.g. `/run/secrets/maven-settings.xml`) and
passed with `mvn -s`, so the locked cache volume keeps caching artifacts
without ever capturing credentials.

## CLI

Local development — credentials come from the developer's Maven setup:

```bash
$ cat dagger.toml
[modules.my-app]
source = "./my-java-module"
entrypoint = true

# Configuration-only entry: loads nothing, applies to any load of this source
[modules."github.com/dagger/java".settings]
mavenSettings = "file://~/.m2/settings.xml"

$ dagger develop      # codegen resolves deps via the private registry
$ dagger call build   # module build does too
```

CI — structured form, no settings.xml anywhere:

```toml
[env.ci.modules."github.com/dagger/java".settings]
mavenSettings = ""   # an empty value in an overlay clears the base setting
registryUrl = "https://artifacts.corp.example/maven"
registryUsername = "ci-bot"
registryPassword = "env://CORP_MAVEN_TOKEN"
```

```bash
$ dagger --env=ci call build
```

Transitive case — `my-app` depends on module `B`, written in Java;
neither `B` nor the java toolchain is called directly. The settings still
apply when `B`'s SDK runtime constructs `Java`.

## Status

Proposal; depends on [Global Module Settings](global-module-settings.md).
Decision points: whether the structured form should support multiple
registries (list-typed settings), and snapshot vs release repository
split in the synthesized profile.

---

- Previous: [Global Module Settings](global-module-settings.md)
