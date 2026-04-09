# Groovy SDK Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a Groovy SDK for Dagger with Gradle build, Groovy AST transformation, and integration tests.

**Architecture:** The Groovy SDK reuses the Java SDK's runtime library (`dagger-java-sdk`) and codegen Maven plugin. New components: Groovy annotations + AST transformation library (`dagger-groovy-sdk`), Go runtime module that orchestrates Maven (for Java SDK deps) + Gradle (for user modules), and Groovy templates with `containerEcho`/`grepDir`.

**Tech Stack:** Groovy 4.x, Java 17, Gradle 8.x with Shadow plugin, Go (runtime module), Maven (Java SDK deps build)

**Design spec:** `hack/designs/groovy-sdk.md`

---

## File Map

### New files to create:

**Engine integration (2 files to modify):**
- `core/sdk/consts.go` — Add `sdkGroovy` constant
- `core/sdk/loader.go` — Add `case sdkGroovy:` in switch + version defaulting

**SDK module manifest:**
- `sdk/groovy/dagger.json` — SDK module config

**Go runtime module:**
- `sdk/groovy/runtime/main.go` — `GroovySdk` struct with `Codegen()` and `ModuleRuntime()`
- `sdk/groovy/runtime/go.mod` — Go module definition
- `sdk/groovy/runtime/images/gradle/Dockerfile` — Gradle + JDK 21 build image
- `sdk/groovy/runtime/images/java/Dockerfile` — JRE 21 runtime image

**Groovy SDK library (annotations + AST transformation):**
- `sdk/groovy/dagger-groovy-sdk/build.gradle` — Gradle build for the library
- `sdk/groovy/dagger-groovy-sdk/settings.gradle` — Gradle settings
- `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Module.groovy`
- `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Object.groovy`
- `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Function.groovy`
- `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Default.groovy`
- `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/DefaultPath.groovy`
- `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Ignore.groovy`
- `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Enum.groovy`
- `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/transform/DaggerEntrypointGenerator.groovy` — Generates Entrypoint source code as a string
- `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/transform/DaggerModuleASTTransformation.groovy` — Global AST transformation entry point
- `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/transform/DaggerTypeMapper.groovy` — Maps Groovy/Java types to Dagger TypeDefKind
- `sdk/groovy/dagger-groovy-sdk/src/main/resources/META-INF/groovy/org.codehaus.groovy.transform.ASTTransformation` — Service loader for AST transformation

**Template files:**
- `sdk/groovy/runtime/template/build.gradle`
- `sdk/groovy/runtime/template/settings.gradle`
- `sdk/groovy/runtime/template/src/main/groovy/io/dagger/modules/daggermodule/DaggerModule.groovy`

**Integration tests:**
- `core/integration/module_groovy_test.go`

---

### Task 1: Engine Registration

Register `groovy` as a built-in SDK name in the Dagger engine.

**Files:**
- Modify: `core/sdk/consts.go`
- Modify: `core/sdk/loader.go`

- [ ] **Step 1: Add sdkGroovy constant to consts.go**

In `core/sdk/consts.go`, add `sdkGroovy` to the const block and to `validInbuiltSDKs`:

```go
// In the const block, after sdkJava:
sdkGroovy sdk = "groovy"

// In validInbuiltSDKs, add sdkGroovy at the end:
var validInbuiltSDKs = []sdk{
    sdkGo,
    sdkDang,
    sdkPython,
    sdkTypescript,
    sdkPHP,
    sdkElixir,
    sdkJava,
    sdkGroovy,
}
```

- [ ] **Step 2: Add case sdkGroovy to loader.go namedSDK switch**

In `core/sdk/loader.go`, in the `namedSDK` function, add after the `case sdkJava:` block (around line 138):

```go
case sdkGroovy:
    return l.SDKForModule(ctx, root, &core.SDKConfig{Source: "github.com/dagger/dagger/sdk/groovy" + sdkSuffix, Config: sdk.Config, Experimental: sdk.Experimental}, nil)
```

- [ ] **Step 3: Add sdkGroovy to version-defaulting list**

In `core/sdk/loader.go`, in the `parseSDKName` function, add `sdkGroovy` to the version-defaulting slice (around line 234):

```go
// Change this line:
if slices.Contains([]sdk{sdkPHP, sdkElixir, sdkJava}, sdk(sdkNameParsed)) && sdkVersion == "" {
// To:
if slices.Contains([]sdk{sdkPHP, sdkElixir, sdkJava, sdkGroovy}, sdk(sdkNameParsed)) && sdkVersion == "" {
```

- [ ] **Step 4: Verify engine compiles**

Run from repo root:
```bash
cd core/sdk && go build ./...
```
Expected: compiles without errors.

- [ ] **Step 5: Commit**

```bash
stg new engine-groovy-registration -m "core/sdk: register groovy as built-in SDK name

Signed-off-by: Yves Brissaud <yves@dagger.io>"
git add core/sdk/consts.go core/sdk/loader.go
stg refresh
```

---

### Task 2: SDK Module Manifest and Container Images

Create the `sdk/groovy/` directory structure with the module manifest and Dockerfiles.

**Files:**
- Create: `sdk/groovy/dagger.json`
- Create: `sdk/groovy/runtime/images/gradle/Dockerfile`
- Create: `sdk/groovy/runtime/images/java/Dockerfile`

- [ ] **Step 1: Create sdk/groovy/dagger.json**

```json
{
  "name": "groovy-sdk",
  "engineVersion": "v0.20.3",
  "sdk": {
    "source": "go"
  },
  "source": "runtime"
}
```

- [ ] **Step 2: Create Gradle build image Dockerfile**

Create `sdk/groovy/runtime/images/gradle/Dockerfile`:

```dockerfile
FROM gradle:8.12-jdk21@sha256:c85bcb75e043a038e08a4e7562e03644580b31c9d7e760a1be099f24a0e57a69
```

Note: Use a pinned digest. The exact digest should be verified at implementation time by pulling the image and noting the digest.

- [ ] **Step 3: Create JRE runtime image Dockerfile**

Create `sdk/groovy/runtime/images/java/Dockerfile` (same as Java SDK):

```dockerfile
FROM eclipse-temurin:21-jre-alpine@sha256:4e9ab608d97796571b1d5bbcd1c9f430a89a5f03fe5aa6c093888ceb6756c502
```

- [ ] **Step 4: Commit**

```bash
stg new groovy-sdk-manifest -m "sdk/groovy: add module manifest and container images

Signed-off-by: Yves Brissaud <yves@dagger.io>"
git add sdk/groovy/dagger.json sdk/groovy/runtime/images/
stg refresh
```

---

### Task 3: Groovy Annotations

Define the Groovy annotation types that module authors will use.

**Files:**
- Create: `sdk/groovy/dagger-groovy-sdk/build.gradle`
- Create: `sdk/groovy/dagger-groovy-sdk/settings.gradle`
- Create: `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Module.groovy`
- Create: `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Object.groovy`
- Create: `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Function.groovy`
- Create: `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Default.groovy`
- Create: `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/DefaultPath.groovy`
- Create: `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Ignore.groovy`
- Create: `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Enum.groovy`

- [ ] **Step 1: Create dagger-groovy-sdk/build.gradle**

```groovy
plugins {
    id 'groovy'
    id 'maven-publish'
}

group = 'io.dagger'
version = '0.0.1-SNAPSHOT'

java {
    toolchain {
        languageVersion = JavaLanguageVersion.of(17)
    }
}

repositories {
    mavenLocal()
    mavenCentral()
}

dependencies {
    implementation 'org.apache.groovy:groovy:4.0.24'
}

publishing {
    publications {
        maven(MavenPublication) {
            artifactId = 'dagger-groovy-sdk'
            from components.java
        }
    }
}
```

- [ ] **Step 2: Create dagger-groovy-sdk/settings.gradle**

```groovy
rootProject.name = 'dagger-groovy-sdk'
```

- [ ] **Step 3: Create annotation files**

Create `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Module.groovy`:

```groovy
package io.dagger.groovy.annotation

import java.lang.annotation.ElementType
import java.lang.annotation.Retention
import java.lang.annotation.RetentionPolicy
import java.lang.annotation.Target

@Target([ElementType.PACKAGE, ElementType.TYPE])
@Retention(RetentionPolicy.SOURCE)
@interface Module {
    String description() default ''
}
```

Create `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Object.groovy`:

```groovy
package io.dagger.groovy.annotation

import java.lang.annotation.ElementType
import java.lang.annotation.Retention
import java.lang.annotation.RetentionPolicy
import java.lang.annotation.Target

@Target(ElementType.TYPE)
@Retention(RetentionPolicy.SOURCE)
@interface Object {
    String value() default ''
}
```

Create `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Function.groovy`:

```groovy
package io.dagger.groovy.annotation

import java.lang.annotation.ElementType
import java.lang.annotation.Retention
import java.lang.annotation.RetentionPolicy
import java.lang.annotation.Target

@Target(ElementType.METHOD)
@Retention(RetentionPolicy.SOURCE)
@interface Function {
    String value() default ''
}
```

Create `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Default.groovy`:

```groovy
package io.dagger.groovy.annotation

import java.lang.annotation.ElementType
import java.lang.annotation.Retention
import java.lang.annotation.RetentionPolicy
import java.lang.annotation.Target

@Target(ElementType.PARAMETER)
@Retention(RetentionPolicy.SOURCE)
@interface Default {
    String value()
}
```

Create `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/DefaultPath.groovy`:

```groovy
package io.dagger.groovy.annotation

import java.lang.annotation.ElementType
import java.lang.annotation.Retention
import java.lang.annotation.RetentionPolicy
import java.lang.annotation.Target

@Target(ElementType.PARAMETER)
@Retention(RetentionPolicy.SOURCE)
@interface DefaultPath {
    String value()
}
```

Create `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Ignore.groovy`:

```groovy
package io.dagger.groovy.annotation

import java.lang.annotation.ElementType
import java.lang.annotation.Retention
import java.lang.annotation.RetentionPolicy
import java.lang.annotation.Target

@Target(ElementType.PARAMETER)
@Retention(RetentionPolicy.SOURCE)
@interface Ignore {
    String[] value()
}
```

Create `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/annotation/Enum.groovy`:

```groovy
package io.dagger.groovy.annotation

import java.lang.annotation.ElementType
import java.lang.annotation.Retention
import java.lang.annotation.RetentionPolicy
import java.lang.annotation.Target

@Target(ElementType.TYPE)
@Retention(RetentionPolicy.SOURCE)
@interface Enum {
}
```

- [ ] **Step 4: Commit**

```bash
stg new groovy-annotations -m "sdk/groovy: add Groovy annotation definitions

Signed-off-by: Yves Brissaud <yves@dagger.io>"
git add sdk/groovy/dagger-groovy-sdk/
stg refresh
```

---

### Task 4: DaggerTypeMapper

The type mapper converts Groovy/Java type names to Dagger TypeDef code snippets. This is used by the AST transformation to generate the `register()` method.

**Files:**
- Create: `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/transform/DaggerTypeMapper.groovy`

- [ ] **Step 1: Create DaggerTypeMapper.groovy**

This class maps Groovy type names to Dagger TypeDef code strings. It mirrors the logic in `sdk/java/dagger-java-annotation-processor/src/main/java/io/dagger/annotation/processor/DaggerType.java` but generates code strings instead of JavaPoet `CodeBlock` objects.

```groovy
package io.dagger.groovy.transform

class DaggerTypeMapper {

    static Set<String> knownEnums = [] as Set

    /**
     * Returns a code string like:
     *   dag().typeDef().withKind(TypeDefKind.STRING_KIND)
     *   dag().typeDef().withObject("Container")
     *   dag().typeDef().withListOf(dag().typeDef().withKind(TypeDefKind.STRING_KIND))
     *   dag().typeDef().withEnum("Severity")
     */
    static String toDaggerTypeDef(String typeName) {
        if (knownEnums.contains(typeName)) {
            String simpleName = typeName.substring(typeName.lastIndexOf('.') + 1)
            return "dag().typeDef().withEnum(\"${simpleName}\")"
        }

        switch (typeName) {
            case 'void':
                return 'dag().typeDef().withKind(TypeDefKind.VOID_KIND).withOptional(true)'
            case 'boolean':
            case 'java.lang.Boolean':
            case 'Boolean':
                return 'dag().typeDef().withKind(TypeDefKind.BOOLEAN_KIND)'
            case 'int':
            case 'long':
            case 'short':
            case 'byte':
            case 'java.lang.Integer':
            case 'Integer':
            case 'java.lang.Long':
            case 'Long':
            case 'java.lang.Short':
            case 'Short':
            case 'java.lang.Byte':
            case 'Byte':
                return 'dag().typeDef().withKind(TypeDefKind.INTEGER_KIND)'
            case 'float':
            case 'double':
            case 'java.lang.Float':
            case 'Float':
            case 'java.lang.Double':
            case 'Double':
                return 'dag().typeDef().withKind(TypeDefKind.FLOAT_KIND)'
            case 'java.lang.String':
            case 'String':
                return 'dag().typeDef().withKind(TypeDefKind.STRING_KIND)'
        }

        // List types: java.util.List<X>
        if (typeName.startsWith('java.util.List<')) {
            String inner = typeName.substring('java.util.List<'.length(), typeName.length() - 1)
            return "dag().typeDef().withListOf(${toDaggerTypeDef(inner)})"
        }

        // Array types: X[]
        if (typeName.endsWith('[]')) {
            String inner = typeName.substring(0, typeName.length() - 2)
            return "dag().typeDef().withListOf(${toDaggerTypeDef(inner)})"
        }

        // Optional types: java.util.Optional<X>
        if (typeName.startsWith('java.util.Optional<')) {
            String inner = typeName.substring('java.util.Optional<'.length(), typeName.length() - 1)
            return toDaggerTypeDef(inner)
        }

        // Dagger object types (Container, Directory, File, etc.)
        String simpleName = typeName.substring(typeName.lastIndexOf('.') + 1)
        return "dag().typeDef().withObject(\"${simpleName}\")"
    }

    /**
     * Returns the Java class literal for deserialization, e.g. "String.class", "Container.class"
     */
    static String toClassLiteral(String typeName) {
        switch (typeName) {
            case 'boolean':
                return 'boolean.class'
            case 'int':
                return 'int.class'
            case 'long':
                return 'long.class'
            case 'float':
                return 'float.class'
            case 'double':
                return 'double.class'
        }

        if (typeName.startsWith('java.util.List<')) {
            String inner = typeName.substring('java.util.List<'.length(), typeName.length() - 1)
            String innerSimple = inner.substring(inner.lastIndexOf('.') + 1)
            return "${innerSimple}[].class"
        }

        if (typeName.endsWith('[]')) {
            String inner = typeName.substring(0, typeName.length() - 2)
            String innerSimple = inner.substring(inner.lastIndexOf('.') + 1)
            return "${innerSimple}[].class"
        }

        String simpleName = typeName.substring(typeName.lastIndexOf('.') + 1)
        return "${simpleName}.class"
    }

    /**
     * Returns true if the type is a list or array type
     */
    static boolean isList(String typeName) {
        return typeName.startsWith('java.util.List<') || typeName.endsWith('[]')
    }
}
```

- [ ] **Step 2: Commit**

```bash
stg new groovy-type-mapper -m "sdk/groovy: add DaggerTypeMapper for type-to-TypeDef mapping

Signed-off-by: Yves Brissaud <yves@dagger.io>"
git add sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/transform/DaggerTypeMapper.groovy
stg refresh
```

---

### Task 5: Entrypoint Generator

The generator produces the Java source code for the `Entrypoint` class. It's called by the AST transformation with collected module metadata.

**Files:**
- Create: `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/transform/DaggerEntrypointGenerator.groovy`

- [ ] **Step 1: Create DaggerEntrypointGenerator.groovy**

This class generates the `Entrypoint.java` source as a string, matching the structure that the Java annotation processor generates (see `sdk/java/dagger-java-annotation-processor/src/test/resources/io/dagger/gen/entrypoint/Entrypoint.java` for reference).

The generator takes structured metadata (collected by the AST transformation) and produces complete Java source code with `main()`, `dispatch()`, `register()`, and `invoke()` methods.

```groovy
package io.dagger.groovy.transform

/**
 * Generates the Entrypoint.java source code from module metadata.
 *
 * The generated Entrypoint follows the same contract as the Java SDK annotation processor:
 * - main() -> dispatch() -> register() or invoke()
 * - register() builds TypeDefs and returns ModuleID
 * - invoke() deserializes parent, calls the right method, serializes result
 */
class DaggerEntrypointGenerator {

    static class ObjectMeta {
        String name
        String qualifiedName
        String description = ''
        List<FunctionMeta> functions = []
        List<FieldMeta> fields = []
        ConstructorMeta constructor
        boolean isMainObject
    }

    static class FunctionMeta {
        String name           // Dagger function name (camelCase)
        String methodName     // Groovy method name
        String description = ''
        String returnType     // Fully qualified return type
        List<ParameterMeta> parameters = []
    }

    static class ParameterMeta {
        String name
        String description = ''
        String type           // Fully qualified type
        boolean optional = false
        String defaultValue   // JSON string or null
        String defaultPath    // Path string or null
        String[] ignore       // Glob patterns or null
    }

    static class FieldMeta {
        String name
        String description = ''
        String type
    }

    static class ConstructorMeta {
        String description = ''
        List<ParameterMeta> parameters = []
    }

    static class EnumMeta {
        String name
        String description = ''
        List<EnumValueMeta> values = []
    }

    static class EnumValueMeta {
        String value
        String description = ''
    }

    static String generate(
        String moduleDescription,
        List<ObjectMeta> objects,
        List<EnumMeta> enums
    ) {
        StringBuilder sb = new StringBuilder()

        sb.append('// This class has been generated by dagger-groovy-sdk. DO NOT EDIT.\n')
        sb.append('package io.dagger.gen.entrypoint;\n\n')
        sb.append('import static io.dagger.client.Dagger.dag;\n\n')

        // Collect all imports needed
        Set<String> imports = new TreeSet<>()
        imports.add('io.dagger.client.FunctionCall')
        imports.add('io.dagger.client.FunctionCallArgValue')
        imports.add('io.dagger.client.JSON')
        imports.add('io.dagger.client.JsonConverter')
        imports.add('io.dagger.client.Module')
        imports.add('io.dagger.client.ModuleID')
        imports.add('io.dagger.client.TypeDef')
        imports.add('io.dagger.client.TypeDefKind')
        imports.add('io.dagger.client.Function')
        imports.add('io.dagger.client.exception.DaggerExecException')
        imports.add('io.dagger.client.exception.DaggerQueryException')
        imports.add('io.dagger.client.telemetry.Telemetry')
        imports.add('java.lang.reflect.InvocationTargetException')
        imports.add('java.util.Arrays')
        imports.add('java.util.HashMap')
        imports.add('java.util.List')
        imports.add('java.util.Map')
        imports.add('java.util.Objects')
        imports.add('java.util.Optional')
        imports.add('java.util.concurrent.ExecutionException')

        // Add imports for all object types referenced
        for (ObjectMeta obj : objects) {
            imports.add(obj.qualifiedName)
            for (FunctionMeta fn : obj.functions) {
                addTypeImport(imports, fn.returnType)
                for (ParameterMeta p : fn.parameters) {
                    addTypeImport(imports, p.type)
                }
            }
            for (FieldMeta f : obj.fields) {
                addTypeImport(imports, f.type)
            }
            if (obj.constructor) {
                for (ParameterMeta p : obj.constructor.parameters) {
                    addTypeImport(imports, p.type)
                }
            }
        }

        for (String imp : imports) {
            sb.append("import ${imp};\n")
        }

        sb.append('\npublic class Entrypoint {\n')
        sb.append('  Entrypoint() {}\n\n')

        // main()
        sb.append('  public static void main(String[] args) throws Exception {\n')
        sb.append('    try (Telemetry telemetry = new Telemetry()) {\n')
        sb.append('      new Entrypoint().dispatch(dag().currentFunctionCall());\n')
        sb.append('    } finally {\n')
        sb.append('      dag().close();\n')
        sb.append('    }\n')
        sb.append('  }\n\n')

        // dispatch()
        sb.append('  private Void dispatch(FunctionCall fnCall) throws Exception {\n')
        sb.append('    try {\n')
        sb.append('      String parentName = fnCall.parentName();\n')
        sb.append('      String fnName = fnCall.name();\n')
        sb.append('      JSON parentJson = fnCall.parent();\n')
        sb.append('      List<FunctionCallArgValue> fnArgs = fnCall.inputArgs();\n')
        sb.append('      Map<String, JSON> inputArgs = new HashMap<>();\n')
        sb.append('      for (FunctionCallArgValue fnArg : fnArgs) {\n')
        sb.append('        inputArgs.put(fnArg.name(), fnArg.value());\n')
        sb.append('      }\n')
        sb.append('      JSON result;\n')
        sb.append('      if (parentName.isEmpty()) {\n')
        sb.append('        ModuleID modID = register();\n')
        sb.append('        result = JsonConverter.toJSON(modID);\n')
        sb.append('      } else {\n')
        sb.append('        result = invoke(parentJson, parentName, fnName, inputArgs);\n')
        sb.append('      }\n')
        sb.append('      fnCall.returnValue(result);\n')
        sb.append('      return null;\n')
        sb.append('    } catch (InvocationTargetException e) {\n')
        sb.append('      fnCall.returnError(dag().error(e.getTargetException().getMessage()));\n')
        sb.append('      throw e;\n')
        sb.append('    } catch (DaggerExecException e) {\n')
        sb.append('      fnCall.returnError(dag().error(e.getMessage())')
        sb.append('.withValue("stdout", JsonConverter.toJSON(e.getStdOut()))')
        sb.append('.withValue("stderr", JsonConverter.toJSON(e.getStdErr()))')
        sb.append('.withValue("cmd", JsonConverter.toJSON(e.getCmd()))')
        sb.append('.withValue("exitCode", JsonConverter.toJSON(e.getExitCode()))')
        sb.append('.withValue("path", JsonConverter.toJSON(e.getPath())));\n')
        sb.append('      throw e;\n')
        sb.append('    } catch (Exception e) {\n')
        sb.append('      fnCall.returnError(dag().error(e.getMessage()));\n')
        sb.append('      throw e;\n')
        sb.append('    }\n')
        sb.append('  }\n\n')

        // register()
        sb.append(generateRegister(moduleDescription, objects, enums))

        // invoke()
        sb.append(generateInvoke(objects))

        sb.append('}\n')
        return sb.toString()
    }

    private static String generateRegister(
        String moduleDescription,
        List<ObjectMeta> objects,
        List<EnumMeta> enums
    ) {
        StringBuilder sb = new StringBuilder()
        sb.append('  private ModuleID register()\n')
        sb.append('      throws ExecutionException, DaggerQueryException, InterruptedException {\n')
        sb.append('    Module module = dag().module()')

        if (moduleDescription) {
            sb.append(".withDescription(\"${escapeJava(moduleDescription)}\")")
        }

        for (ObjectMeta obj : objects) {
            sb.append('.withObject(dag()\n')
            sb.append('        .typeDef()\n')
            sb.append("        .withObject(\"${obj.name}\"")
            if (obj.description) {
                sb.append(",\n            new TypeDef.WithObjectArguments().withDescription(\"${escapeJava(obj.description)}\")")
            }
            sb.append(')\n')

            // Functions
            for (FunctionMeta fn : obj.functions) {
                sb.append("        .withFunction(dag().function(\"${fn.name}\", ${DaggerTypeMapper.toDaggerTypeDef(fn.returnType)})\n")
                if (fn.description) {
                    sb.append("            .withDescription(\"${escapeJava(fn.description)}\")\n")
                }
                for (ParameterMeta p : fn.parameters) {
                    String typeDef = DaggerTypeMapper.toDaggerTypeDef(p.type)
                    if (p.optional) {
                        typeDef += '.withOptional(true)'
                    }
                    sb.append("            .withArg(\"${p.name}\", ${typeDef}")
                    if (p.defaultValue || p.defaultPath || p.ignore || p.description) {
                        sb.append(",\n                new Function.WithArgArguments()")
                        if (p.description) {
                            sb.append(".withDescription(\"${escapeJava(p.description)}\")")
                        }
                        if (p.defaultValue) {
                            sb.append(".withDefaultValue(JSON.from(\"${escapeJava(p.defaultValue)}\"))")
                        }
                        if (p.defaultPath) {
                            sb.append(".withDefaultPath(\"${escapeJava(p.defaultPath)}\")")
                        }
                        if (p.ignore) {
                            String ignoreList = p.ignore.collect { "\"${escapeJava(it)}\"" }.join(', ')
                            sb.append(".withIgnore(List.of(${ignoreList}))")
                        }
                    }
                    sb.append(')\n')
                }
                sb.append('        )\n')
            }

            // Fields
            for (FieldMeta f : obj.fields) {
                sb.append("        .withField(\"${f.name}\", ${DaggerTypeMapper.toDaggerTypeDef(f.type)}")
                if (f.description) {
                    sb.append(",\n            new TypeDef.WithFieldArguments().withDescription(\"${escapeJava(f.description)}\")")
                }
                sb.append(')\n')
            }

            // Constructor (only for main object)
            if (obj.constructor && obj.isMainObject) {
                sb.append("        .withConstructor(dag().function(\"\", ${DaggerTypeMapper.toDaggerTypeDef(obj.qualifiedName)})\n")
                if (obj.constructor.description) {
                    sb.append("            .withDescription(\"${escapeJava(obj.constructor.description)}\")\n")
                }
                for (ParameterMeta p : obj.constructor.parameters) {
                    String typeDef = DaggerTypeMapper.toDaggerTypeDef(p.type)
                    if (p.optional) {
                        typeDef += '.withOptional(true)'
                    }
                    sb.append("            .withArg(\"${p.name}\", ${typeDef}")
                    if (p.defaultValue || p.defaultPath || p.ignore || p.description) {
                        sb.append(",\n                new Function.WithArgArguments()")
                        if (p.description) {
                            sb.append(".withDescription(\"${escapeJava(p.description)}\")")
                        }
                        if (p.defaultValue) {
                            sb.append(".withDefaultValue(JSON.from(\"${escapeJava(p.defaultValue)}\"))")
                        }
                        if (p.defaultPath) {
                            sb.append(".withDefaultPath(\"${escapeJava(p.defaultPath)}\")")
                        }
                        if (p.ignore) {
                            String ignoreList = p.ignore.collect { "\"${escapeJava(it)}\"" }.join(', ')
                            sb.append(".withIgnore(List.of(${ignoreList}))")
                        }
                    }
                    sb.append(')\n')
                }
                sb.append('        )\n')
            }

            sb.append('    )\n')
        }

        // Enums
        for (EnumMeta e : enums) {
            sb.append('        .withEnum(dag().typeDef()\n')
            sb.append("            .withEnum(\"${e.name}\"")
            if (e.description) {
                sb.append(", new TypeDef.WithEnumArguments().withDescription(\"${escapeJava(e.description)}\")")
            }
            sb.append(')\n')
            for (EnumValueMeta v : e.values) {
                sb.append("            .withEnumValue(\"${v.value}\"")
                if (v.description) {
                    sb.append(", new TypeDef.WithEnumValueArguments().withDescription(\"${escapeJava(v.description)}\")")
                }
                sb.append(')\n')
            }
            sb.append('        )\n')
        }

        sb.append(';\n')
        sb.append('    return module.id();\n')
        sb.append('  }\n\n')
        return sb.toString()
    }

    private static String generateInvoke(List<ObjectMeta> objects) {
        StringBuilder sb = new StringBuilder()
        sb.append('  private JSON invoke(JSON parentJson, String parentName, String fnName,\n')
        sb.append('      Map<String, JSON> inputArgs) throws Exception {\n')

        for (ObjectMeta obj : objects) {
            sb.append("    if (parentName.equals(\"${obj.name}\")) {\n")

            for (FunctionMeta fn : obj.functions) {
                sb.append("      if (fnName.equals(\"${fn.name}\")) {\n")
                sb.append("        Class clazz = Class.forName(\"${obj.qualifiedName}\");\n")

                String simpleName = obj.qualifiedName.substring(obj.qualifiedName.lastIndexOf('.') + 1)
                sb.append("        ${simpleName} obj = (${simpleName}) JsonConverter.fromJSON(parentJson, clazz);\n")

                // Deserialize parameters
                for (ParameterMeta p : fn.parameters) {
                    String pSimple = simpleTypeName(p.type)
                    if (isPrimitive(p.type)) {
                        sb.append("        ${p.type} ${p.name} = ${primitiveDefault(p.type)};\n")
                    } else {
                        sb.append("        ${pSimple} ${p.name} = null;\n")
                    }
                    sb.append("        if (inputArgs.get(\"${p.name}\") != null) {\n")
                    sb.append("          ${p.name} = JsonConverter.fromJSON(inputArgs.get(\"${p.name}\"), ${DaggerTypeMapper.toClassLiteral(p.type)});\n")
                    sb.append('        }\n')

                    if (DaggerTypeMapper.isList(p.type) && !p.optional) {
                        // Wrap array result into List
                        sb.append("        Objects.requireNonNull(${p.name}, \"${p.name} must not be null\");\n")
                    } else if (p.optional) {
                        sb.append("        var ${p.name}_opt = Optional.ofNullable(${p.name});\n")
                    } else if (!isPrimitive(p.type)) {
                        sb.append("        Objects.requireNonNull(${p.name}, \"${p.name} must not be null\");\n")
                    }
                }

                // Call method
                String args = fn.parameters.collect { p ->
                    if (p.optional) {
                        "${p.name}_opt"
                    } else if (DaggerTypeMapper.isList(p.type)) {
                        "Arrays.asList(${p.name})"
                    } else {
                        p.name
                    }
                }.join(', ')

                String retSimple = simpleTypeName(fn.returnType)
                if (fn.returnType == 'void') {
                    sb.append("        obj.${fn.methodName}(${args});\n")
                    sb.append('        return JsonConverter.toJSON(null);\n')
                } else {
                    sb.append("        ${retSimple} res = obj.${fn.methodName}(${args});\n")
                    sb.append('        return JsonConverter.toJSON(res);\n')
                }
                sb.append('      }\n')
            }

            // Constructor (empty fnName)
            if (obj.constructor && obj.isMainObject) {
                sb.append('      if (fnName.equals("")) {\n')
                for (ParameterMeta p : obj.constructor.parameters) {
                    String pSimple = simpleTypeName(p.type)
                    if (isPrimitive(p.type)) {
                        sb.append("        ${p.type} ${p.name} = ${primitiveDefault(p.type)};\n")
                    } else {
                        sb.append("        ${pSimple} ${p.name} = null;\n")
                    }
                    sb.append("        if (inputArgs.get(\"${p.name}\") != null) {\n")
                    sb.append("          ${p.name} = JsonConverter.fromJSON(inputArgs.get(\"${p.name}\"), ${DaggerTypeMapper.toClassLiteral(p.type)});\n")
                    sb.append('        }\n')
                    if (p.optional) {
                        sb.append("        var ${p.name}_opt = Optional.ofNullable(${p.name});\n")
                    } else if (!isPrimitive(p.type)) {
                        sb.append("        Objects.requireNonNull(${p.name}, \"${p.name} must not be null\");\n")
                    }
                }

                String ctorArgs = obj.constructor.parameters.collect { p ->
                    p.optional ? "${p.name}_opt" : p.name
                }.join(', ')

                String simpleName = obj.qualifiedName.substring(obj.qualifiedName.lastIndexOf('.') + 1)
                sb.append("        ${simpleName} res = new ${simpleName}(${ctorArgs});\n")
                sb.append('        return JsonConverter.toJSON(res);\n')
                sb.append('      }\n')
            }

            sb.append('    }\n')
        }

        sb.append('    throw new InvocationTargetException(new Error("unknown function " + fnName));\n')
        sb.append('  }\n')
        return sb.toString()
    }

    private static void addTypeImport(Set<String> imports, String typeName) {
        if (!typeName || isPrimitive(typeName) || typeName == 'void') return
        if (typeName.startsWith('java.util.List<')) {
            String inner = typeName.substring('java.util.List<'.length(), typeName.length() - 1)
            addTypeImport(imports, inner)
            return
        }
        if (typeName.startsWith('java.util.Optional<')) {
            String inner = typeName.substring('java.util.Optional<'.length(), typeName.length() - 1)
            addTypeImport(imports, inner)
            return
        }
        if (typeName.endsWith('[]')) {
            addTypeImport(imports, typeName.substring(0, typeName.length() - 2))
            return
        }
        if (typeName.contains('.') && !typeName.startsWith('java.lang.')) {
            imports.add(typeName)
        }
    }

    private static String simpleTypeName(String typeName) {
        if (typeName.startsWith('java.util.List<')) {
            return typeName // keep full for List
        }
        return typeName.substring(typeName.lastIndexOf('.') + 1)
    }

    private static boolean isPrimitive(String typeName) {
        return typeName in ['boolean', 'int', 'long', 'short', 'byte', 'float', 'double', 'char']
    }

    private static String primitiveDefault(String typeName) {
        switch (typeName) {
            case 'boolean': return 'false'
            case 'float':
            case 'double': return '0'
            default: return '0'
        }
    }

    private static String escapeJava(String s) {
        if (!s) return ''
        return s.replace('\\', '\\\\')
                .replace('"', '\\"')
                .replace('\n', '\\n')
                .replace('\r', '\\r')
                .replace('\t', '\\t')
    }
}
```

- [ ] **Step 2: Commit**

```bash
stg new groovy-entrypoint-gen -m "sdk/groovy: add DaggerEntrypointGenerator for Entrypoint.java generation

Signed-off-by: Yves Brissaud <yves@dagger.io>"
git add sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/transform/DaggerEntrypointGenerator.groovy
stg refresh
```

---

### Task 6: AST Transformation

The global AST transformation that scans Groovy classes for `@Object`/`@Function`/`@Enum` annotations, collects metadata, and writes the generated `Entrypoint.java` file.

**Files:**
- Create: `sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/transform/DaggerModuleASTTransformation.groovy`
- Create: `sdk/groovy/dagger-groovy-sdk/src/main/resources/META-INF/groovy/org.codehaus.groovy.transform.ASTTransformation`

- [ ] **Step 1: Create the service loader registration**

Create `sdk/groovy/dagger-groovy-sdk/src/main/resources/META-INF/groovy/org.codehaus.groovy.transform.ASTTransformation`:

```
io.dagger.groovy.transform.DaggerModuleASTTransformation
```

This file tells the Groovy compiler to discover and apply the AST transformation globally.

- [ ] **Step 2: Create DaggerModuleASTTransformation.groovy**

```groovy
package io.dagger.groovy.transform

import org.codehaus.groovy.ast.ASTNode
import org.codehaus.groovy.ast.AnnotationNode
import org.codehaus.groovy.ast.ClassNode
import org.codehaus.groovy.ast.FieldNode
import org.codehaus.groovy.ast.MethodNode
import org.codehaus.groovy.ast.ModuleNode
import org.codehaus.groovy.ast.Parameter
import org.codehaus.groovy.ast.expr.ConstantExpression
import org.codehaus.groovy.ast.expr.ListExpression
import org.codehaus.groovy.control.CompilePhase
import org.codehaus.groovy.control.SourceUnit
import org.codehaus.groovy.transform.ASTTransformation
import org.codehaus.groovy.transform.GroovyASTTransformation

import io.dagger.groovy.transform.DaggerEntrypointGenerator.ObjectMeta
import io.dagger.groovy.transform.DaggerEntrypointGenerator.FunctionMeta
import io.dagger.groovy.transform.DaggerEntrypointGenerator.ParameterMeta
import io.dagger.groovy.transform.DaggerEntrypointGenerator.FieldMeta
import io.dagger.groovy.transform.DaggerEntrypointGenerator.ConstructorMeta
import io.dagger.groovy.transform.DaggerEntrypointGenerator.EnumMeta
import io.dagger.groovy.transform.DaggerEntrypointGenerator.EnumValueMeta

/**
 * Global AST transformation that scans for @Object, @Function, @Enum annotations
 * and generates an Entrypoint.java file for the Dagger runtime.
 */
@GroovyASTTransformation(phase = CompilePhase.SEMANTIC_ANALYSIS)
class DaggerModuleASTTransformation implements ASTTransformation {

    private static final String OBJECT_ANNOTATION = 'io.dagger.groovy.annotation.Object'
    private static final String FUNCTION_ANNOTATION = 'io.dagger.groovy.annotation.Function'
    private static final String ENUM_ANNOTATION = 'io.dagger.groovy.annotation.Enum'
    private static final String MODULE_ANNOTATION = 'io.dagger.groovy.annotation.Module'
    private static final String DEFAULT_ANNOTATION = 'io.dagger.groovy.annotation.Default'
    private static final String DEFAULT_PATH_ANNOTATION = 'io.dagger.groovy.annotation.DefaultPath'
    private static final String IGNORE_ANNOTATION = 'io.dagger.groovy.annotation.Ignore'

    // Track whether we've already generated for this compilation
    private static boolean generated = false

    @Override
    void visit(ASTNode[] nodes, SourceUnit sourceUnit) {
        if (generated) return

        ModuleNode moduleNode = sourceUnit.AST
        if (!moduleNode) return

        String moduleName = System.getenv('_DAGGER_GROOVY_SDK_MODULE_NAME')
        if (!moduleName) return // Not running inside dagger codegen

        List<ObjectMeta> objects = []
        List<EnumMeta> enums = []
        String moduleDescription = ''
        Set<String> enumNames = [] as Set

        // Scan all classes in the compilation unit
        for (ClassNode classNode : moduleNode.classes) {
            // Check for @Module on class (Groovy allows this instead of package-info)
            AnnotationNode moduleAnnotation = findAnnotation(classNode, MODULE_ANNOTATION)
            if (moduleAnnotation) {
                moduleDescription = getAnnotationStringValue(moduleAnnotation, 'description', '')
            }

            // Check for @Enum
            AnnotationNode enumAnnotation = findAnnotation(classNode, ENUM_ANNOTATION)
            if (enumAnnotation && classNode.isEnum()) {
                EnumMeta enumMeta = new EnumMeta()
                enumMeta.name = classNode.nameWithoutPackage
                enumMeta.description = extractGroovyDoc(classNode)

                for (FieldNode field : classNode.fields) {
                    if (field.isEnum()) {
                        EnumValueMeta valueMeta = new EnumValueMeta()
                        valueMeta.value = field.name
                        valueMeta.description = '' // Groovy enums don't have per-constant javadoc easily
                        enumMeta.values.add(valueMeta)
                    }
                }

                enums.add(enumMeta)
                enumNames.add(classNode.name)
            }

            // Check for @Object
            AnnotationNode objectAnnotation = findAnnotation(classNode, OBJECT_ANNOTATION)
            if (objectAnnotation) {
                ObjectMeta objMeta = new ObjectMeta()

                String customName = getAnnotationStringValue(objectAnnotation, 'value', '')
                objMeta.name = customName ?: classNode.nameWithoutPackage
                objMeta.qualifiedName = classNode.name
                objMeta.description = extractGroovyDoc(classNode)
                objMeta.isMainObject = areSimilar(objMeta.name, moduleName)

                // Collect @Function methods
                for (MethodNode method : classNode.methods) {
                    AnnotationNode fnAnnotation = findAnnotation(method, FUNCTION_ANNOTATION)
                    if (fnAnnotation) {
                        // Validate explicit return type
                        if (method.returnType.name == 'java.lang.Object' && !method.returnType.isResolved()) {
                            sourceUnit.addError(new org.codehaus.groovy.syntax.SyntaxException(
                                "@Function methods must have explicit return types: ${classNode.name}.${method.name}",
                                method.lineNumber, method.columnNumber))
                            continue
                        }

                        FunctionMeta fnMeta = new FunctionMeta()
                        String fnCustomName = getAnnotationStringValue(fnAnnotation, 'value', '')
                        fnMeta.name = fnCustomName ?: method.name
                        fnMeta.methodName = method.name
                        fnMeta.description = extractGroovyDoc(method)
                        fnMeta.returnType = resolveTypeName(method.returnType)

                        for (Parameter param : method.parameters) {
                            ParameterMeta pMeta = buildParameterMeta(param)
                            fnMeta.parameters.add(pMeta)
                        }

                        objMeta.functions.add(fnMeta)
                    }
                }

                // Collect exposed fields (public non-static non-transient)
                for (FieldNode field : classNode.fields) {
                    if (field.isStatic() || field.name.startsWith('$') || field.name.startsWith('__')) continue
                    boolean isPublic = field.isPublic()
                    boolean hasFunctionAnnotation = findAnnotation(field, FUNCTION_ANNOTATION) != null
                    if (isPublic || hasFunctionAnnotation) {
                        FieldMeta fMeta = new FieldMeta()
                        fMeta.name = field.name
                        fMeta.description = extractGroovyDoc(field)
                        fMeta.type = resolveTypeName(field.type)
                        objMeta.fields.add(fMeta)
                    }
                }

                // Check for constructor with parameters (main object only)
                if (objMeta.isMainObject) {
                    List<org.codehaus.groovy.ast.ConstructorNode> constructors = classNode.declaredConstructors
                    List<org.codehaus.groovy.ast.ConstructorNode> nonEmpty = constructors.findAll { it.parameters.length > 0 }
                    if (nonEmpty.size() == 1) {
                        ConstructorMeta ctorMeta = new ConstructorMeta()
                        ctorMeta.description = extractGroovyDoc(nonEmpty[0])
                        for (Parameter param : nonEmpty[0].parameters) {
                            ctorMeta.parameters.add(buildParameterMeta(param))
                        }
                        objMeta.constructor = ctorMeta
                    } else if (nonEmpty.size() > 1) {
                        sourceUnit.addError(new org.codehaus.groovy.syntax.SyntaxException(
                            "Main object ${classNode.name} must have at most one non-empty constructor",
                            classNode.lineNumber, classNode.columnNumber))
                    }
                }

                objects.add(objMeta)
            }
        }

        if (objects.isEmpty()) return

        // Set known enums for type mapping
        DaggerTypeMapper.knownEnums = enumNames

        // Generate the Entrypoint.java source
        String entrypointSource = DaggerEntrypointGenerator.generate(moduleDescription, objects, enums)

        // Write to build/generated/sources/dagger/io/dagger/gen/entrypoint/Entrypoint.java
        String outputDir = System.getProperty('dagger.groovy.generated.dir',
            'build/generated/sources/dagger')
        File outputFile = new File(outputDir, 'io/dagger/gen/entrypoint/Entrypoint.java')
        outputFile.parentFile.mkdirs()
        outputFile.text = entrypointSource

        generated = true
    }

    private ParameterMeta buildParameterMeta(Parameter param) {
        ParameterMeta pMeta = new ParameterMeta()
        pMeta.name = param.name
        pMeta.type = resolveTypeName(param.type)

        // Check for Optional type
        if (param.type.name == 'java.util.Optional') {
            pMeta.optional = true
            if (param.type.genericsTypes) {
                pMeta.type = resolveTypeName(param.type.genericsTypes[0].type)
            }
        }

        // Check for @Default
        AnnotationNode defaultAnn = findAnnotation(param, DEFAULT_ANNOTATION)
        if (defaultAnn) {
            pMeta.defaultValue = getAnnotationStringValue(defaultAnn, 'value', null)
        }

        // Check for @DefaultPath
        AnnotationNode defaultPathAnn = findAnnotation(param, DEFAULT_PATH_ANNOTATION)
        if (defaultPathAnn) {
            pMeta.defaultPath = getAnnotationStringValue(defaultPathAnn, 'value', null)
        }

        // Check for @Ignore
        AnnotationNode ignoreAnn = findAnnotation(param, IGNORE_ANNOTATION)
        if (ignoreAnn) {
            def valueExpr = ignoreAnn.getMember('value')
            if (valueExpr instanceof ListExpression) {
                pMeta.ignore = valueExpr.expressions.collect { ((ConstantExpression) it).value.toString() } as String[]
            } else if (valueExpr instanceof ConstantExpression) {
                pMeta.ignore = [valueExpr.value.toString()] as String[]
            }
        }

        return pMeta
    }

    private static AnnotationNode findAnnotation(ASTNode node, String annotationName) {
        if (node instanceof ClassNode) {
            return node.annotations.find { it.classNode.name == annotationName }
        } else if (node instanceof MethodNode) {
            return node.annotations.find { it.classNode.name == annotationName }
        } else if (node instanceof FieldNode) {
            return node.annotations.find { it.classNode.name == annotationName }
        } else if (node instanceof Parameter) {
            return node.annotations.find { it.classNode.name == annotationName }
        }
        return null
    }

    private static String getAnnotationStringValue(AnnotationNode ann, String member, String defaultValue) {
        def expr = ann.getMember(member)
        if (expr instanceof ConstantExpression) {
            return expr.value?.toString() ?: defaultValue
        }
        return defaultValue
    }

    private static String resolveTypeName(ClassNode classNode) {
        if (!classNode) return 'java.lang.Object'
        String name = classNode.name
        // Handle generics (e.g., List<String>)
        if (classNode.genericsTypes) {
            String inner = classNode.genericsTypes.collect { resolveTypeName(it.type) }.join(', ')
            return "${name}<${inner}>"
        }
        return name
    }

    private static String extractGroovyDoc(ASTNode node) {
        // Groovy AST doesn't preserve Javadoc/GroovyDoc in a structured way
        // in the annotation processing phase. Return empty for now.
        // GroovyDoc support can be enhanced later.
        return ''
    }

    /**
     * Compare names ignoring case and hyphens/underscores, matching the Java SDK behavior.
     */
    private static boolean areSimilar(String name1, String name2) {
        if (!name1 || !name2) return false
        String normalize(String s) {
            s.toLowerCase().replaceAll('[-_]', '')
        }
        return normalize(name1) == normalize(name2)
    }
}
```

- [ ] **Step 3: Commit**

```bash
stg new groovy-ast-transform -m "sdk/groovy: add AST transformation for Dagger module processing

Signed-off-by: Yves Brissaud <yves@dagger.io>"
git add sdk/groovy/dagger-groovy-sdk/src/main/groovy/io/dagger/groovy/transform/DaggerModuleASTTransformation.groovy
git add sdk/groovy/dagger-groovy-sdk/src/main/resources/
stg refresh
```

---

### Task 7: Template Files

Create the template files that `dagger init --sdk=groovy` scaffolds into a new module.

**Files:**
- Create: `sdk/groovy/runtime/template/build.gradle`
- Create: `sdk/groovy/runtime/template/settings.gradle`
- Create: `sdk/groovy/runtime/template/src/main/groovy/io/dagger/modules/daggermodule/DaggerModule.groovy`

- [ ] **Step 1: Create template/build.gradle**

```groovy
plugins {
    id 'groovy'
    id 'com.gradleup.shadow' version '9.0.0-beta12'
}

group = 'io.dagger.modules.daggermodule'
version = '1.0-SNAPSHOT'

java {
    toolchain {
        languageVersion = JavaLanguageVersion.of(17)
    }
}

repositories {
    mavenLocal()
    mavenCentral()
}

dependencies {
    implementation "io.dagger:dagger-java-sdk:${findProperty('daggerDepsVersion') ?: '0.0.1-SNAPSHOT'}"
    implementation "io.dagger:dagger-groovy-sdk:${findProperty('daggerDepsVersion') ?: '0.0.1-SNAPSHOT'}"
    implementation 'org.apache.groovy:groovy:4.0.24'
    runtimeOnly 'org.slf4j:slf4j-simple:2.0.16'
    runtimeOnly 'org.eclipse:yasson:3.0.4'
    implementation 'io.netty:netty-handler:4.1.125.Final'
    runtimeOnly 'net.minidev:json-smart:2.5.2'
}

compileGroovy {
    groovyOptions.configurationScript = file("${projectDir}/groovy-compiler-config.groovy")
    options.compilerArgs += [
        '-Adagger.groovy.generated.dir=' + layout.buildDirectory.dir('generated/sources/dagger').get().asFile.absolutePath
    ]
}

sourceSets {
    main {
        java {
            srcDir layout.buildDirectory.dir('generated/sources/dagger')
        }
    }
}

shadowJar {
    archiveClassifier = ''
    manifest {
        attributes 'Main-Class': 'io.dagger.gen.entrypoint.Entrypoint'
    }
    mergeServiceFiles()
}
```

- [ ] **Step 2: Create template/settings.gradle**

```groovy
rootProject.name = 'dagger-module-placeholder'
```

- [ ] **Step 3: Create template DaggerModule.groovy**

Create `sdk/groovy/runtime/template/src/main/groovy/io/dagger/modules/daggermodule/DaggerModule.groovy`:

```groovy
package io.dagger.modules.daggermoduleplaceholder

import io.dagger.client.Container
import io.dagger.client.Directory
import io.dagger.groovy.annotation.Object
import io.dagger.groovy.annotation.Function
import static io.dagger.client.Dagger.dag

/** DaggerModule main object */
@Object
class DaggerModule {
    /** Returns a container that echoes whatever string argument is provided */
    @Function
    Container containerEcho(String stringArg) {
        dag().container().from('alpine:latest').withExec(['echo', stringArg])
    }

    /** Returns lines that match a pattern in the files of the provided Directory */
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

- [ ] **Step 4: Commit**

```bash
stg new groovy-template -m "sdk/groovy: add module template for dagger init

Signed-off-by: Yves Brissaud <yves@dagger.io>"
git add sdk/groovy/runtime/template/
stg refresh
```

---

### Task 8: Go Runtime Module

The Go runtime module that implements `Codegen()` and `ModuleRuntime()` for the Groovy SDK.

**Files:**
- Create: `sdk/groovy/runtime/main.go`
- Create: `sdk/groovy/runtime/go.mod`

- [ ] **Step 1: Create go.mod**

Create `sdk/groovy/runtime/go.mod`. Start from the Java SDK's go.mod as reference (`sdk/java/runtime/go.mod`) but with module name `groovy-sdk`:

```
module groovy-sdk

go 1.25.0
```

Then run from `sdk/groovy/runtime/`:
```bash
cd sdk/groovy/runtime && go mod tidy
```

This will resolve the exact dependency versions from the repo.

Note: The `go.mod` needs `dagger.io/dagger` and `github.com/iancoleman/strcase` as direct dependencies, plus the same otel replace directives as the Java SDK's go.mod. Copy the full dependency block from `sdk/java/runtime/go.mod` as a starting point, then run `go mod tidy` to reconcile.

- [ ] **Step 2: Create main.go**

Create `sdk/groovy/runtime/main.go`:

```go
// Runtime module for the Groovy SDK
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	_ "embed"

	"groovy-sdk/internal/dagger"

	"github.com/iancoleman/strcase"
)

const (
	ModSourceDirPath = "/src"
	ModDirPath       = "/opt/module"
	GenPath          = "/dagger-io"
	GroovySDKPath    = "/dagger-groovy-sdk"
)

type GroovySdk struct {
	SDKSourceDir    *dagger.Directory
	JavaSDKSourceDir *dagger.Directory
	moduleConfig    moduleConfig
	GradleDebugLogging bool
}

type moduleConfig struct {
	name    string
	subPath string
	dirPath string
}

func (c *moduleConfig) modulePath() string {
	return filepath.Join(ModSourceDirPath, c.subPath)
}

func New(
	// Directory with the Groovy SDK source code.
	// +defaultPath="/sdk/groovy"
	// +ignore=["**", "!dagger-groovy-sdk/", "!runtime/template/", "!runtime/images/"]
	sdkSourceDir *dagger.Directory,
	// Directory with the Java SDK source code (needed to build Java dependencies).
	// +defaultPath="/sdk/java"
	// +ignore=["**", "!dagger-codegen-maven-plugin/", "!dagger-java-annotation-processor/", "!dagger-java-sdk/", "!dagger-java-samples/pom.xml", "!LICENSE", "!README.md", "!pom.xml", "**/src/test", "**/target"]
	javaSDKSourceDir *dagger.Directory,
) (*GroovySdk, error) {
	if sdkSourceDir == nil {
		return nil, fmt.Errorf("groovy sdk source directory not provided")
	}
	if javaSDKSourceDir == nil {
		return nil, fmt.Errorf("java sdk source directory not provided")
	}
	return &GroovySdk{
		SDKSourceDir:    sdkSourceDir,
		JavaSDKSourceDir: javaSDKSourceDir,
	}, nil
}

func (m *GroovySdk) WithConfig(
	// +default=false
	gradleDebugLogging bool,
) *GroovySdk {
	m.GradleDebugLogging = gradleDebugLogging
	return m
}

func (m *GroovySdk) Codegen(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.GeneratedCode, error) {
	if err := m.setModuleConfig(ctx, modSource); err != nil {
		return nil, err
	}

	ctr, err := m.codegenBase(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}

	generatedCode, err := m.generateCode(ctx, ctr, introspectionJSON)
	if err != nil {
		return nil, err
	}

	return dag.
		GeneratedCode(dag.Directory().WithDirectory("/", generatedCode)).
		WithVCSGeneratedPaths([]string{
			"build/generated/**",
		}).
		WithVCSIgnoredPaths([]string{
			"build",
			".gradle",
		}), nil
}

func (m *GroovySdk) codegenBase(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	ctr, err := m.buildAllDependencies(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}
	ctr = ctr.
		WithDirectory(ModSourceDirPath, modSource.ContextDirectory()).
		WithWorkdir(m.moduleConfig.modulePath())

	ctr, err = m.addTemplate(ctx, ctr)
	if err != nil {
		return nil, err
	}

	// Set the dagger deps version in the user's build
	version, err := m.getDaggerVersionForModule(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}
	ctr = ctr.WithEnvVariable("DAGGER_DEPS_VERSION", version)

	return ctr, nil
}

// buildAllDependencies builds both Java SDK deps (via Maven) and Groovy SDK (via Gradle),
// installing everything to the shared Maven local repo at /root/.m2
func (m *GroovySdk) buildAllDependencies(
	ctx context.Context,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	javaDeps, err := m.buildJavaDependencies(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}

	// Now build the Groovy SDK using the Gradle container, sharing the same .m2
	groovyCtr, err := m.gradleContainer(ctx)
	if err != nil {
		return nil, err
	}

	version, err := m.getDaggerVersionForModule(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}

	groovyCtr = groovyCtr.
		// Share the .m2 cache that now contains Java SDK jars
		WithMountedCache("/root/.m2", dag.CacheVolume("sdk-groovy-maven-m2"), dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked}).
		// Import the Java deps into the shared .m2
		WithDirectory("/root/.m2", javaDeps.Directory("/root/.m2")).
		// Mount the Groovy SDK source
		WithDirectory(GroovySDKPath, m.SDKSourceDir.Directory("dagger-groovy-sdk")).
		WithWorkdir(GroovySDKPath).
		// Build and publish to maven local
		WithExec(m.gradleCommand(
			"gradle",
			"publishToMavenLocal",
			"-PdaggerDepsVersion="+version,
		))

	return groovyCtr, nil
}

// buildJavaDependencies builds the Java SDK submodules with Maven
func (m *GroovySdk) buildJavaDependencies(
	ctx context.Context,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	mvnCtr, err := m.mvnContainer(ctx)
	if err != nil {
		return nil, err
	}
	version, err := m.getDaggerVersionForModule(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}
	return mvnCtr.
		WithMountedCache("/root/.m2", dag.CacheVolume("sdk-groovy-maven-m2"), dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked}).
		WithMountedFile("/schema.json", introspectionJSON).
		WithDirectory(GenPath, m.JavaSDKSourceDir).
		WithWorkdir(GenPath).
		WithExec([]string{
			"mvn",
			"versions:set",
			"-DgenerateBackupPoms=false",
			fmt.Sprintf("-DnewVersion=%s", version),
			"--no-transfer-progress",
		}).
		WithExec([]string{
			"mvn",
			"--projects", "dagger-codegen-maven-plugin,dagger-java-annotation-processor,dagger-java-sdk", "--also-make",
			"clean", "install",
			"-DskipTests",
			"-Ddaggerengine.schema=/schema.json",
			"--no-transfer-progress",
		}), nil
}

func (m *GroovySdk) addTemplate(
	ctx context.Context,
	ctr *dagger.Container,
) (*dagger.Container, error) {
	name := m.moduleConfig.name
	pkgName := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(name), "-", ""), "_", "")
	kebabName := strcase.ToKebab(name)
	camelName := strcase.ToCamel(name)

	// Check if there's a build.gradle inside the module path
	if _, err := ctr.File(filepath.Join(m.moduleConfig.modulePath(), "build.gradle")).Name(ctx); err == nil {
		return ctr, nil
	}

	absPath := func(rel ...string) string {
		return filepath.Join(append([]string{m.moduleConfig.modulePath()}, rel...)...)
	}

	changes := []repl{
		{"dagger-module-placeholder", kebabName},
		{"daggermoduleplaceholder", pkgName},
	}

	templateDir := dag.CurrentModule().Source().Directory("template")
	buildGradle, err := m.replace(ctx, templateDir, "build.gradle", changes...)
	if err != nil {
		return ctr, fmt.Errorf("could not add template: %w", err)
	}
	settingsGradle, err := m.replace(ctx, templateDir, "settings.gradle", changes...)
	if err != nil {
		return ctr, fmt.Errorf("could not add template: %w", err)
	}

	changes = append(changes, repl{"DaggerModule", camelName})
	daggerModuleGroovy, err := m.replace(ctx, templateDir,
		filepath.Join("src", "main", "groovy", "io", "dagger", "modules", "daggermodule", "DaggerModule.groovy"),
		changes...)
	if err != nil {
		return ctr, fmt.Errorf("could not add template: %w", err)
	}

	ctr = ctr.
		WithNewFile(absPath("build.gradle"), buildGradle).
		WithNewFile(absPath("settings.gradle"), settingsGradle).
		WithNewFile(absPath("src", "main", "groovy", "io", "dagger", "modules", pkgName, fmt.Sprintf("%s.groovy", camelName)), daggerModuleGroovy)

	return ctr, nil
}

func (m *GroovySdk) generateCode(
	ctx context.Context,
	ctr *dagger.Container,
	introspectionJSON *dagger.File,
) (*dagger.Directory, error) {
	version, err := m.getDaggerVersionForModule(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}

	compiled := ctr.
		WithEnvVariable("_DAGGER_GROOVY_SDK_MODULE_NAME", m.moduleConfig.name).
		WithExec(m.gradleCommand(
			"gradle",
			"compileGroovy", "compileJava",
			"-PdaggerDepsVersion="+version,
		))

	return dag.
		Directory().
		// Copy all user files
		WithDirectory(
			m.moduleConfig.modulePath(),
			ctr.Directory(m.moduleConfig.modulePath())).
		// Copy generated sources
		WithDirectory(
			filepath.Join(m.moduleConfig.modulePath(), "build", "generated", "sources", "dagger"),
			compiled.Directory(filepath.Join(m.moduleConfig.modulePath(), "build", "generated", "sources", "dagger"))).
		Directory(ModSourceDirPath), nil
}

func (m *GroovySdk) ModuleRuntime(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	if err := m.setModuleConfig(ctx, modSource); err != nil {
		return nil, err
	}

	ctr, err := m.codegenBase(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}

	jar, err := m.buildJar(ctx, ctr, introspectionJSON)
	if err != nil {
		return nil, err
	}

	javaCtr, err := m.jreContainer(ctx)
	if err != nil {
		return nil, err
	}
	javaCtr = javaCtr.
		WithFile(filepath.Join(ModDirPath, "module.jar"), jar).
		WithWorkdir(ModDirPath).
		WithEntrypoint([]string{"java", "-jar", filepath.Join(ModDirPath, "module.jar")})

	return javaCtr, nil
}

func (m *GroovySdk) buildJar(
	ctx context.Context,
	ctr *dagger.Container,
	introspectionJSON *dagger.File,
) (*dagger.File, error) {
	version, err := m.getDaggerVersionForModule(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}
	built := ctr.
		WithEnvVariable("_DAGGER_GROOVY_SDK_MODULE_NAME", m.moduleConfig.name).
		WithExec(m.gradleCommand(
			"gradle",
			"shadowJar",
			"-PdaggerDepsVersion="+version,
		))

	// The shadow jar is at build/libs/<name>-<version>.jar
	// We need to find it
	return built.File(filepath.Join(m.moduleConfig.modulePath(), "build", "libs", "*.jar")), nil
}

func (m *GroovySdk) setModuleConfig(ctx context.Context, modSource *dagger.ModuleSource) error {
	modName, err := modSource.ModuleName(ctx)
	if err != nil {
		return err
	}
	subPath, err := modSource.SourceSubpath(ctx)
	if err != nil {
		return err
	}
	var dirPath string
	if kind, err := modSource.Kind(ctx); err != nil {
		return err
	} else if kind == dagger.ModuleSourceKindLocal {
		dirPath, err = modSource.LocalContextDirectoryPath(ctx)
		if err != nil {
			return err
		}
	}
	m.moduleConfig = moduleConfig{
		name:    modName,
		subPath: subPath,
		dirPath: dirPath,
	}
	return nil
}

func (m *GroovySdk) getDaggerVersionForModule(ctx context.Context, introspectionJSON *dagger.File) (string, error) {
	content, err := introspectionJSON.Contents(ctx)
	if err != nil {
		return "", err
	}
	var introspectJSON IntrospectJSON
	if err = json.Unmarshal([]byte(content), &introspectJSON); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"%s-%s-module",
		strings.TrimPrefix(introspectJSON.SchemaVersion, "v"),
		m.moduleConfig.name,
	), nil
}

type IntrospectJSON struct {
	SchemaVersion string `json:"__schemaVersion"`
}

type repl struct {
	oldString string
	newString string
}

func (m *GroovySdk) replace(
	ctx context.Context,
	dir *dagger.Directory,
	path string,
	changes ...repl,
) (string, error) {
	content, err := dir.File(path).Contents(ctx)
	if err != nil {
		return "", err
	}
	for _, change := range changes {
		content = strings.ReplaceAll(content, change.oldString, change.newString)
	}
	return content, nil
}

func (m *GroovySdk) gradleCommand(args ...string) []string {
	if m.GradleDebugLogging {
		args = append(args, "--debug")
	}
	args = append(args, "--no-daemon")
	return args
}

func (m *GroovySdk) mvnContainer(ctx context.Context) (*dagger.Container, error) {
	return disableSVEOnArm64(ctx, m.MavenImage())
}

func (m *GroovySdk) gradleContainer(ctx context.Context) (*dagger.Container, error) {
	return disableSVEOnArm64(ctx, m.GradleImage())
}

func (m *GroovySdk) jreContainer(ctx context.Context) (*dagger.Container, error) {
	return disableSVEOnArm64(ctx, m.JavaImage())
}

func disableSVEOnArm64(ctx context.Context, ctr *dagger.Container) (*dagger.Container, error) {
	if platform, err := ctr.Platform(ctx); err != nil {
		return nil, err
	} else if strings.Contains(string(platform), "arm64") {
		return ctr.WithEnvVariable("_JAVA_OPTIONS", "-XX:UseSVE=0"), nil
	}
	return ctr, nil
}

//go:embed images/gradle/Dockerfile
var gradleImage string

func (m *GroovySdk) GradleImage() *dagger.Container {
	return dag.Directory().WithNewFile("Dockerfile", gradleImage).DockerBuild()
}

// Maven image for building Java SDK dependencies
// Reuse the same image as the Java SDK
func (m *GroovySdk) MavenImage() *dagger.Container {
	return dag.Directory().WithNewFile("Dockerfile", "FROM maven:3.9.9-eclipse-temurin-21-alpine@sha256:4cbb8bf76c46b97e028998f2486ed014759a8e932480431039bdb93dffe6813e\n").DockerBuild()
}

//go:embed images/java/Dockerfile
var javaImage string

func (m *GroovySdk) JavaImage() *dagger.Container {
	return dag.Directory().WithNewFile("Dockerfile", javaImage).DockerBuild()
}
```

- [ ] **Step 3: Initialize Go module**

```bash
cd sdk/groovy/runtime
go mod init groovy-sdk
```

Then copy the dependency block from `sdk/java/runtime/go.mod` (the `require` blocks and `replace` directives), and run:

```bash
cd sdk/groovy/runtime && go mod tidy
```

- [ ] **Step 4: Generate dagger types**

Run dagger develop to generate the `internal/dagger` package:

```bash
cd sdk/groovy && dagger develop
```

This generates `sdk/groovy/runtime/internal/dagger/` with all the Go client types needed.

- [ ] **Step 5: Verify the Go module compiles**

```bash
cd sdk/groovy/runtime && go build ./...
```

Expected: compiles without errors.

- [ ] **Step 6: Commit**

```bash
stg new groovy-go-runtime -m "sdk/groovy: add Go runtime module with Codegen and ModuleRuntime

Signed-off-by: Yves Brissaud <yves@dagger.io>"
git add sdk/groovy/runtime/ sdk/groovy/dagger.json
stg refresh
```

---

### Task 9: Integration Tests

Add Go integration tests for the Groovy SDK, following the Java test patterns.

**Files:**
- Create: `core/integration/module_groovy_test.go`

- [ ] **Step 1: Create module_groovy_test.go**

```go
package core

import (
	"path/filepath"
	"testing"

	"context"

	"dagger.io/dagger"
	"github.com/stretchr/testify/require"

	"github.com/dagger/testctx"
)

type GroovySuite struct{}

func TestGroovy(t *testing.T) {
	testctx.New(t, Middleware()...).RunTests(GroovySuite{})
}

func (GroovySuite) TestInit(_ context.Context, t *testctx.T) {
	t.Run("from alias", func(ctx context.Context, t *testctx.T) {
		c := connect(ctx, t)

		modGen := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work").
			With(daggerExec("init", "--name=bare", "--sdk=groovy"))

		out, err := modGen.
			With(daggerQuery(`{containerEcho(stringArg:"hello"){stdout}}`)).
			Stdout(ctx)
		require.NoError(t, err)
		require.JSONEq(t, `{"containerEcho":{"stdout":"hello\n"}}`, out)
	})

	t.Run("from upstream", func(ctx context.Context, t *testctx.T) {
		c := connect(ctx, t)

		modGen := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work").
			With(daggerExec("init", "--name=bare", "--sdk=github.com/dagger/dagger/sdk/groovy"))

		out, err := modGen.
			With(daggerQuery(`{containerEcho(stringArg:"hello"){stdout}}`)).
			Stdout(ctx)
		require.NoError(t, err)
		require.JSONEq(t, `{"containerEcho":{"stdout":"hello\n"}}`, out)
	})

	t.Run("from alias with ref", func(ctx context.Context, t *testctx.T) {
		c := connect(ctx, t)

		modGen := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work").
			With(daggerExec("init", "--name=bare", "--sdk=groovy@main"))

		out, err := modGen.
			With(daggerQuery(`{containerEcho(stringArg:"hello"){stdout}}`)).
			Stdout(ctx)
		require.NoError(t, err)
		require.JSONEq(t, `{"containerEcho":{"stdout":"hello\n"}}`, out)
	})

	t.Run("grep-dir", func(ctx context.Context, t *testctx.T) {
		c := connect(ctx, t)

		modGen := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work").
			With(daggerExec("init", "--name=bare", "--sdk=groovy"))

		out, err := modGen.
			With(daggerCall("grep-dir", "--directory-arg=.", "--pattern=dagger")).
			Stdout(ctx)
		require.NoError(t, err)
		require.Contains(t, out, "dagger")
	})
}

func groovyModule(t *testctx.T, c *dagger.Client, moduleName string) *dagger.Container {
	t.Helper()
	modSrc, err := filepath.Abs(filepath.Join("./testdata/modules/groovy", moduleName))
	require.NoError(t, err)

	groovySdkSrc, err := filepath.Abs("../../sdk/groovy")
	require.NoError(t, err)

	javaSdkSrc, err := filepath.Abs("../../sdk/java")
	require.NoError(t, err)

	return goGitBase(t, c).
		WithDirectory("modules/"+moduleName, c.Host().Directory(modSrc)).
		WithDirectory("sdk/groovy", c.Host().Directory(groovySdkSrc)).
		WithDirectory("sdk/java", c.Host().Directory(javaSdkSrc)).
		WithWorkdir("/work/modules/" + moduleName)
}
```

- [ ] **Step 2: Verify test compiles**

```bash
cd core/integration && go build ./...
```

Expected: compiles without errors.

- [ ] **Step 3: Run the TestInit test**

```bash
cd core/integration && go test -run TestGroovy/TestInit -v -count=1 -timeout=600s
```

Expected: All TestInit subtests pass (this exercises the full pipeline: engine → Go runtime → Maven build → Gradle build → template scaffold → module execution).

Note: This test requires a running Dagger engine. If running in CI, the test infrastructure handles this. Locally, ensure `dagger` CLI is available and the engine is running.

- [ ] **Step 4: Commit**

```bash
stg new groovy-integration-tests -m "core/integration: add Groovy SDK integration tests

Signed-off-by: Yves Brissaud <yves@dagger.io>"
git add core/integration/module_groovy_test.go
stg refresh
```

---

### Task 10: End-to-End Validation and Fixes

Run the full test suite, fix any issues discovered, and validate the complete pipeline works.

- [ ] **Step 1: Run integration tests**

```bash
cd core/integration && go test -run TestGroovy -v -count=1 -timeout=600s
```

If tests fail, read the error output carefully. Common issues:
- Go module compilation errors in `sdk/groovy/runtime/` → fix `main.go` or `go.mod`
- Gradle build failures → check template `build.gradle` dependency versions
- AST transformation not finding annotations → check `META-INF/groovy/` service file
- Entrypoint generation errors → check `DaggerEntrypointGenerator.groovy` output
- Shadow JAR glob not finding the file → adjust the `buildJar` file path pattern

- [ ] **Step 2: Fix any failing tests**

Address each failure individually, re-running the specific failing test after each fix.

- [ ] **Step 3: Run the full test once more**

```bash
cd core/integration && go test -run TestGroovy -v -count=1 -timeout=600s
```

Expected: All tests pass.

- [ ] **Step 4: Commit any fixes**

```bash
stg new groovy-fixes -m "sdk/groovy: fix issues found during integration testing

Signed-off-by: Yves Brissaud <yves@dagger.io>"
git add -A
stg refresh
```

---

## Task Dependency Graph

```
Task 1 (Engine Registration)
    ↓
Task 2 (Manifest + Images)
    ↓
Task 3 (Annotations) ──→ Task 4 (TypeMapper) ──→ Task 5 (Entrypoint Gen) ──→ Task 6 (AST Transform)
                                                                                      ↓
Task 7 (Template) ──────────────────────────────────────────────────────────→ Task 8 (Go Runtime)
                                                                                      ↓
                                                                              Task 9 (Integration Tests)
                                                                                      ↓
                                                                              Task 10 (E2E Validation)
```

Tasks 1-2 can run in parallel. Tasks 3-6 are sequential (each builds on the previous). Task 7 is independent until Task 8. Tasks 8-10 are sequential.
