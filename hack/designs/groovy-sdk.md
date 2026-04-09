# Groovy SDK Design

## Overview

A Dagger SDK for the Groovy language, integrated like Java (not embedded like
TypeScript/Python). Reuses the Java SDK runtime library (`dagger-java-sdk`) and
codegen Maven plugin, but provides Groovy-native annotations, an AST
transformation (replacing the Java annotation processor), Gradle as the build
tool, and Groovy templates.

## Goals

- Groovy-native developer experience: Gradle build, Groovy annotations, AST
  transformation, idiomatic Groovy templates
- Reuse Java SDK infrastructure where it makes sense (runtime client library,
  GraphQL schema codegen)
- Integrated SDK (resolved from GitHub at engine tag, not embedded in engine
  binary)
- Include default templates: `containerEcho` and `grepDir`
- Integration tests following the Java SDK test patterns

## Non-Goals

- Porting the Java SDK client library to Groovy
- Porting the codegen Maven plugin to Gradle
- Embedded SDK (baked into the engine container)

## Technology Choices

- **Groovy 4.x** (`org.apache.groovy` group ID)
- **Java 17** target (matching Java SDK)
- **Gradle** with Shadow plugin for fat JAR packaging
- **Groovy AST transformation** instead of Java APT

## Architecture

### Directory Structure

```
sdk/groovy/
  dagger.json                         # {name: "groovy-sdk", sdk: {source: "go"}, source: "runtime"}
  runtime/
    main.go                           # Go Dagger module: GroovySdk
    go.mod / go.sum
    template/
      build.gradle                    # Gradle build for new user modules
      settings.gradle
      src/main/groovy/io/dagger/modules/daggermodule/DaggerModule.groovy
    images/
      gradle/Dockerfile              # gradle:8.x-jdk21 base image
      java/Dockerfile                # eclipse-temurin:21-jre-alpine runtime image
  dagger-groovy-sdk/
    build.gradle                     # Builds annotation + AST transformation library
    settings.gradle
    src/main/groovy/io/dagger/groovy/
      annotation/
        Module.groovy
        Object.groovy
        Function.groovy
        Default.groovy
        DefaultPath.groovy
        Ignore.groovy
        Enum.groovy
      transform/
        DaggerModuleASTTransformation.groovy
```

### Components

#### 1. `dagger-groovy-sdk` (Groovy Annotation + AST Transformation Library)

A Gradle library project published to local Maven repo. Contains:

- **Annotations** (`io.dagger.groovy.annotation`): `@Module`, `@Object`,
  `@Function`, `@Default`, `@DefaultPath`, `@Ignore`, `@Enum`. Mirror the Java
  SDK annotation semantics but defined as Groovy annotations with `SOURCE`
  retention.
- **AST Transformation** (`io.dagger.groovy.transform`): A global AST
  transformation that runs at compile time. It scans classes for `@Object`,
  collects `@Function` methods and `@Enum` enums, reads
  `_DAGGER_GROOVY_SDK_MODULE_NAME` env var to identify the main object, and
  generates an `Entrypoint` class with `register()`, `invoke()`, and `main()`
  methods.

The generated `Entrypoint` uses `dagger-java-sdk` runtime classes
(`Dagger.dag()`, `QueryBuilder`, `JsonConverter`, etc.) since Groovy interops
with Java seamlessly.

All `@Function`-annotated methods must have explicit return types (no `def`).
The AST transformation validates this and errors if omitted.

#### 2. Go Runtime Module (`runtime/main.go`)

A Go-based Dagger module exposing `GroovySdk` with:

- `Codegen(modSource, introspectionJSON) -> GeneratedCode`
- `ModuleRuntime(modSource, introspectionJSON) -> Container`
- `WithConfig(gradleDebugLogging) -> GroovySdk`

**Constructor parameters:**
- `sdkSourceDir` (`+defaultPath="/sdk/groovy"`) — Groovy SDK source
- `javaSDKSourceDir` (`+defaultPath="/sdk/java"`) — Java SDK source for
  building dependencies

**Build pipeline:**

1. `buildJavaDependencies(introspectionJSON)`: Mount Java SDK source, run
   `mvn install` on the 3 submodules (codegen plugin, annotation processor,
   SDK library) with schema JSON. Installs JARs to `/root/.m2`.

2. `buildGroovyDependencies()`: Mount `dagger-groovy-sdk/`, run
   `gradle publishToMavenLocal`. Installs Groovy annotation + AST
   transformation JAR to `/root/.m2`.

3. `addTemplate(ctr)`: If no `build.gradle` in user module path, scaffold from
   `template/` with name substitutions:
   - `dagger-module-placeholder` -> kebab-case name
   - `daggermoduleplaceholder` -> lowercase package name
   - `DaggerModule` -> CamelCase class name

4. `generateCode(ctr, introspectionJSON)`: Set
   `_DAGGER_GROOVY_SDK_MODULE_NAME` env var, run `gradle compileGroovy`. AST
   transformation generates `Entrypoint` into `build/generated/sources/`.

**Codegen returns:**
- `GeneratedCode` with VCS generated paths: `build/generated/**`
- VCS ignored paths: `build`, `.gradle`

**ModuleRuntime returns:**
- Container based on `eclipse-temurin:21-jre-alpine`
- Fat JAR at `/opt/module/module.jar` (Gradle Shadow plugin)
- Entrypoint: `java -jar /opt/module/module.jar`

#### 3. Template

Scaffolded by `dagger init --sdk=groovy`:

**`build.gradle`**: Groovy plugin + Shadow plugin, dependencies on
`dagger-java-sdk` and `dagger-groovy-sdk` from `mavenLocal()`, shadow JAR
with `Entrypoint` as main class.

**`DaggerModule.groovy`**: Idiomatic Groovy with explicit return types:

```groovy
@Object
class DaggerModule {
    @Function
    Container containerEcho(String stringArg) {
        dag().container().from('alpine:latest').withExec(['echo', stringArg])
    }

    @Function
    String grepDir(Directory directoryArg, String pattern) {
        dag().container()
            .from('alpine:latest')
            .withMountedDirectory('/mnt', directoryArg)
            .withWorkdir('/mnt')
            .withExec(['grep', '-R', pattern, '.'])
            .stdout()
    }
}
```

### Engine Integration

Two files modified:

1. **`core/sdk/consts.go`**: Add `sdkGroovy sdk = "groovy"` constant and add
   to `validInbuiltSDKs` slice.
2. **`core/sdk/loader.go`**: Add `case sdkGroovy:` in `namedSDK` switch
   pointing to `github.com/dagger/dagger/sdk/groovy` + suffix. Add `sdkGroovy`
   to the version-defaulting list alongside `sdkPHP, sdkElixir, sdkJava`.

### Integration Tests

File: `core/integration/module_groovy_test.go`

```go
type GroovySuite struct{}

func TestGroovy(t *testing.T) {
    testctx.New(t, Middleware()...).RunTests(GroovySuite{})
}
```

Helper `groovyModule(t, c, moduleName)` mounts both `../../sdk/groovy` and
`../../sdk/java`.

**Test cases:**
- `TestInit`: `dagger init --sdk=groovy`, query `containerEcho` and `grepDir`
  - Variants: from alias, from full path, from alias with ref
- Additional fixture-based tests can be added later for fields, constructors,
  enums, defaults, etc.

**Test fixtures:** `core/integration/testdata/modules/groovy/` with
`dagger.json` pointing to local SDK source.
