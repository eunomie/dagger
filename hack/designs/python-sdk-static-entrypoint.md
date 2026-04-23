# Python SDK: static runtime entrypoint

## Goal

Move *all* per-module type/dispatch analysis from runtime into codegen.
Python codegen emits `src/<package>/_dagger_main.py` containing:

1. **`build_module(dag)`** — imperative `dag.module().with_object(...)`
   calls produced from AST analysis. Runtime calls this verbatim when
   the engine asks for typedefs.
2. **`invoke(parent_name, fn_name, parent_state, inputs)`** — a
   `match (parent_name, fn_name):` dispatch with one branch per
   (object, function). Each branch imports the user's class lazily,
   rehydrates parent state via hand-written helpers, coerces each
   argument, invokes the user function, and returns the result.

Concrete success criterion: after this spec lands, every existing
well-formed Python module works end-to-end without the runtime ever
walking `__annotations__`, invoking `cattrs`, or interpreting
metadata. `dagger.mod._analyzer` and all analysis code only run at
codegen time.

This is spec 2 of 3. Spec 1 (schematool self-calls) already shipped
on this branch; spec 3 will add `legacyCodegenAtRuntime` opt-in so
`_dagger_main.py` and `gen.py` can be committed and codegen skipped
at runtime.

## Background

Today (post-spec 1), the Python runtime flow for a `dagger call` is:

- `template/runtime.py` (8 lines) → `dagger.mod.cli.app` → `main()`
- For `--register` or empty-parent-name: runs `_analyzer.analyze_module`
  on the user's source, then `register_from_metadata(md)` which makes
  live GraphQL calls to build a `dagger.ModuleID`. Returns that.
- For named function: imports user module (triggers `@object_type` /
  `@function` decorators which populate `__dagger_module__`), then
  `Module.serve()` → `invoke()` walks that registry, coerces args via
  `cattrs`, calls the user function.

Two things analyze the user's module at runtime:
1. `_analyzer.analyze_module` walks the AST every register call.
2. The `@object_type` / `@function` decorators walk `__annotations__`
   + `get_type_hints` at import time to build `__dagger_module__`.

Go's SDK does neither. `cmd/codegen/generator/go/templates/modules.go`
generates `main.go` with an imperative `createMod = dotLine(createMod,
"WithObject").Call(...)` chain — the whole type-registration dance is
written out as literal Go code. Dispatch is a baked-in
`switch parentName { switch fnName { ... } }`. Zero iteration over
metadata at runtime.

Spec 2 brings Python to that model.

## Proposal

### Architecture

```
engine → pythonSDK.Codegen → WithSDK phases:

  Phase 0: analyzer emit --format=metadata,schematool
           → /module-metadata.json    (always)
           → /module-types.json       (self-calls only; consumed by merge-schema)

  Phase 1: merge-schema                        (self-calls only)
           → /extended-schema.json

  Phase 2: codegen generate
           -i /schema.json | /extended-schema.json
           → /gen.py

  Phase 3: analyzer entrypoint --metadata /module-metadata.json
           → /_dagger_main.py          (always — new in spec 2)

WithSource adds:
  sdk/src/dagger/client/gen.py         (client bindings — spec 1 path)
  src/<package>/_dagger_main.py        (entrypoint — new in spec 2)

Runtime /runtime (template/runtime.py rewrite):
  imports <pkg>._dagger_main
  --register / empty parent_name → await entry.build_module(dag)
  named fn                       → await entry.invoke(parent, fn, state, inputs)
  ZERO analysis. ZERO __dagger_module__ walk. ZERO cattrs.
```

The AST walk in phase 0 produces up to **two** output files in a
single CLI invocation: `--metadata-output` (always) and
`--schematool-output` (self-calls only). One analyzer run, two
serializers — the `emit` subcommand is extended with both flags
and writes whichever are passed.

### Components

#### 1. `dagger.mod._analyzer.entrypoint_gen` — the codegen module

New file: `sdk/python/src/dagger/mod/_analyzer/entrypoint_gen.py`.
Pure-function module producing Python source.

Public API:

```python
def generate_entrypoint(metadata: ModuleMetadata) -> str:
    """Return Python source for `_dagger_main.py` given pre-analyzed
    module metadata.  Emits imperative `build_module(dag)` and
    `invoke(...)` functions with a match-statement dispatch.
    """
```

Internal helpers (mirror the `schematool.py` decomposition):

- `_render_build_module(metadata) -> str` — emits the `build_module`
  body with chained `dag.module().with_object(...)` calls.
- `_render_invoke(metadata) -> str` — emits the `invoke` body with a
  `match (parent_name, fn_name):` containing one `case` per
  (object, function) + one `case` per `@dagger.field` readback.
- `_render_typedef_for(resolved_type) -> str` — emits a
  `dag.type_def().with_*(...)` expression for a single type. Recursive
  for list elements, composite for objects/enums.
- `_render_coercion_for(resolved_type, value_expr) -> str` — emits
  the Python expression that coerces a JSON-decoded `value_expr` into
  the target type; chooses the right helper from
  `dagger.mod._entrypoint` based on the type kind.
- `_render_unstructure_for(resolved_type, value_expr) -> str` — emits
  the expression converting a Python return value back to a
  JSON-serializable form.

Imports in the generated file are collected during rendering: user
classes (`from <pkg> import Test, Foo, Color`), helper functions
(`from dagger.mod._entrypoint import ...`), and `dagger` /
`dagger.dag`.

#### 2. `dagger.mod._entrypoint` — hand-written helper module

New file: `sdk/python/src/dagger/mod/_entrypoint.py`. Small, purpose-
built, **no type introspection**. Codegen picks the right helper per
use-site; the helpers themselves do one specific, flat conversion.

```python
def rehydrate_parent(cls: type[T], state: dict) -> T:
    if not state:
        return cls()
    return cls(**state)

def coerce_id(loader: Callable, id_value: str) -> Any:
    return loader(id_value)

def coerce_object(cls: type[T], value: dict) -> T:
    return cls(**value)

def coerce_enum(cls: type[Enum], value: str) -> Enum:
    return cls(value)

def coerce_list(element_coercer: Callable[[Any], T], values: list) -> list[T]:
    return [element_coercer(v) for v in values]

async def unstructure_id(obj) -> str:
    return await obj.id()

def unstructure_dataclass(obj) -> dict:
    return dataclasses.asdict(obj)

def unstructure_enum(obj: Enum) -> str:
    return obj.value
```

Guard test: grep the module source for banned patterns
(`__annotations__`, `get_type_hints`, `cattrs`, `typing.get_*`) and
assert none appear. Enforces helper purity.

#### 3. Generated `_dagger_main.py` — shape

For `class Test` with `container_echo(string_arg: str = "Hi") -> Container`
and `upload(src: Directory, label: str) -> str`:

```python
"""AUTO-GENERATED. Do not edit by hand.
Produced by `dagger.mod._analyzer` from the module's Python source.
"""
import dagger
from dagger import dag
from dagger.mod._entrypoint import (
    rehydrate_parent,
    coerce_id,
    unstructure_id,
)


async def build_module(_dag):
    """Emit the module's typedefs via the live Dagger API."""
    return (
        _dag.module()
        .with_object(
            _dag.type_def().with_object("Test")
            .with_function(
                _dag.function(
                    "containerEcho",
                    _dag.type_def().with_object("Container"),
                )
                .with_arg(
                    "stringArg",
                    _dag.type_def().with_kind(dagger.TypeDefKind.STRING_KIND),
                    default_value=dagger.JSON('"Hi"'),
                )
            )
            .with_function(
                _dag.function(
                    "upload",
                    _dag.type_def().with_kind(dagger.TypeDefKind.STRING_KIND),
                )
                .with_arg("src", _dag.type_def().with_object("Directory"))
                .with_arg(
                    "label",
                    _dag.type_def().with_kind(dagger.TypeDefKind.STRING_KIND),
                )
            )
        )
    )


async def invoke(parent_name, fn_name, parent_state, inputs):
    match (parent_name, fn_name):
        case ("Test", "containerEcho"):
            from main import Test
            inst = rehydrate_parent(Test, parent_state)
            string_arg = inputs.get("stringArg", "Hi")
            result = inst.container_echo(string_arg=string_arg)
            return await unstructure_id(result)

        case ("Test", "upload"):
            from main import Test
            inst = rehydrate_parent(Test, parent_state)
            src = coerce_id(dag.directory_from_id, inputs["src"])
            label = inputs["label"]
            return await inst.upload(src=src, label=label)

        case _:
            raise RuntimeError(
                f"unknown dispatch target: {parent_name!r}.{fn_name!r}"
            )
```

Notes:

- The import `from main import Test` in each case arm is
  illustrative — codegen uses the actual user package name from
  `DAGGER_DEFAULT_PYTHON_PACKAGE` (available at codegen time via
  `m.PackageName`), not the literal string `main`.
- User-class imports are **inside** each `case` arm, not at the
  module top. This keeps `build_module(_dag)` importable without user
  deps available — `--register` works before `uv sync` installs
  user-module deps.
- Constructor dispatch (`fn_name == ""`): `case ("Test", ""):`
  branches call the class directly without `rehydrate_parent`.
- Field readback: `case ("Test", "fieldName"):` branches return
  `getattr(inst, "field_name")` for test-invocation parity with today's
  behavior.
- Return-value unstructuring is also codegen'd: IDs get
  `await unstructure_id(...)`, dataclasses get
  `unstructure_dataclass(...)`, enums get `unstructure_enum(...)`,
  primitives pass through.

#### 4. Runtime rewrite — `sdk/python/runtime/template/runtime.py`

Replaces today's 8-line shim that calls `dagger.mod.cli.app`. New
template:

```python
#!/usr/bin/env python
"""Dagger Python module runtime entry.

Delegates entirely to the codegen'd _dagger_main.py in the user's
module package. No analysis at this layer.
"""
from __future__ import annotations

import importlib
import json
import os
import sys

import anyio
import dagger
from dagger import telemetry


async def main(register: bool) -> int:
    pkg = os.environ.get("DAGGER_DEFAULT_PYTHON_PACKAGE", "main")
    try:
        entry = importlib.import_module(f"{pkg}._dagger_main")
    except ImportError as exc:
        sys.stderr.write(
            f"module codegen missing ({exc}); run `dagger develop`\n"
        )
        return 2

    async with await dagger.connect():
        fn_call = dagger.dag.current_function_call()

        if register:
            module_id = await (await entry.build_module(dagger.dag)).id()
            output_path = os.environ.get("DAGGER_MODULE_FILE", "/module.json")
            await anyio.Path(output_path).write_text(json.dumps(module_id))
            return 0

        parent_name = await fn_call.parent_name()
        if not parent_name:
            module_id = await (await entry.build_module(dagger.dag)).id()
            await fn_call.return_value(dagger.JSON(json.dumps(module_id)))
            return 0

        fn_name = await fn_call.name()
        parent_state = json.loads((await fn_call.parent()) or "{}")
        inputs = {
            (await arg.name()): json.loads(await arg.value())
            for arg in await fn_call.input_args()
        }
        result = await entry.invoke(
            parent_name, fn_name, parent_state, inputs
        )
        await fn_call.return_value(dagger.JSON(json.dumps(result)))
        return 0


if __name__ == "__main__":
    telemetry.initialize()
    try:
        sys.exit(anyio.run(main, "--register" in sys.argv[1:]))
    finally:
        telemetry.shutdown()
```

`dagger.mod.cli.app` stays in the SDK for backwards compat (custom
SDK authors, test utilities) — the runtime template just doesn't use
it anymore.

#### 5. Decorator changes — `dagger.mod._module`

`@object_type` / `@function` / `@field` / `@enum_type` /
`@interface` become minimal markers:

- `@object_type(cls)` applies `@dataclasses.dataclass(cls)` (so
  users still get dataclass behavior) and returns `cls`. No
  `__dagger_module__` construction.
- `@function(fn)` sets `fn.__dagger_is_function__ = True` and returns
  `fn`.
- `@field(...)` returns a `dataclasses.field(...)` with a sentinel
  marker attached.
- `@enum_type`, `@interface` — analogous minimal markers.

The AST analyzer in `_analyzer/parser.py` already detects decorated
members via AST (not via these markers), so behavior is unchanged.
Markers remain useful for:
- User code that wants to check `hasattr(cls, '__dagger_is_function__')`
- Third-party tooling
- Runtime `@function` sanity (e.g., raising on missing decoration)

The existing `Module` / `Module.serve()` / `Module.invoke()` classes
in `dagger.mod._module` and the registration machinery in
`dagger.mod._resolver` remain but are no longer reached from
`template/runtime.py`. Removal is a follow-up cleanup.

#### 6. Codegen pipeline integration — `sdk/python/runtime/main.go::WithSDK`

`WithSDK` gains one new phase. Both paths (self-calls on/off) land:

```go
if introspectionJSON != nil {
    // Phase 0: analyze (one AST walk, two outputs when self-calls is on).
    phase0Args := []string{
        "uv", "run", "--isolated", "--frozen",
        "python", "-m", "dagger.mod._analyzer", "emit",
        "--module-source-dir", userSourceDir,
        "--main-object", m.MainObjectName,
        "--module-name", m.ModName,
        "--metadata-output", "/module-metadata.json",
    }
    if m.SelfCallsEnabled {
        phase0Args = append(phase0Args,
            "--introspection-json", SchemaPath,
            "--schematool-output", "/module-types.json",
        )
    }
    ctr = ctr.WithExec(phase0Args)

    if m.SelfCallsEnabled {
        // Phase 1: merge — unchanged from spec 1
        ctr = ctr.WithExec([]string{"merge-schema", "merge-schema", ...})
        schemaForCodegen = "/extended-schema.json"
    } else {
        schemaForCodegen = SchemaPath
    }

    // Phase 2: codegen — unchanged shape, parameterized schema path
    genFile := ctr.WithExec(append(codegenCmd,
        "generate", "-i", schemaForCodegen, "-o", "/gen.py",
    )).File("/gen.py")

    // Phase 3: entrypoint-gen — NEW in spec 2, always runs
    entryFile := ctr.WithExec([]string{
        "uv", "run", "--isolated", "--frozen",
        "python", "-m", "dagger.mod._analyzer", "entrypoint",
        "--metadata", "/module-metadata.json",
        "--output", "/_dagger_main.py",
    }).File("/_dagger_main.py")

    m.AddFile(genPath, genFile)
    m.AddFile(path.Join(m.SubPath, "src", m.PackageName, "_dagger_main.py"),
              entryFile)
}
```

The existing `runPlainCodegen` / `runSelfCallsCodegen` split from
spec 1 is re-unified into a single `runCodegen` that handles both
cases, since the phase 3 exec is identical regardless. The prebuilt
`.dagger-build/gen.py` short-circuit goes away — now that we always
codegen both the client bindings *and* the entrypoint, the prebuilt
optimization only covers half the work and isn't worth the
conditional complexity. Performance re-optimization is spec 3's
concern (committing both files and skipping codegen entirely).

### Data flow and invariants

See "Architecture" above for the diagram. Invariants the
implementation must preserve:

1. **`_dagger_main.py` is the sole source of typedefs at runtime.**
   The `--register` / empty-parent-name protocol is served by
   `build_module(_dag)`. `_analyzer.ast_register` is no longer
   reached from `template/runtime.py` (it remains in the codebase
   for extension authors).

2. **No user-code import until a user-function is actually invoked.**
   `build_module(_dag)` imports only the helper module and `dagger`.
   User-class imports live inside each `invoke()` case arm.
   `--register` does not require user deps to be installed.

3. **Deterministic codegen output.** `generate_entrypoint(metadata)`
   is pure: same metadata → byte-identical Python source. Metadata
   ordering is stable via dict insertion order. Required for spec 3
   (users commit the file).

4. **Name correspondence.** Identifier names (objects, functions,
   args) are lowerCamelCase — matches the engine schema via the
   `to_api_name` helper added in spec 1.

5. **Match-statement exhaustiveness.** `case _:` fallback raises with
   the unknown `(parent_name, fn_name)` pair. If this triggers, it's
   a codegen bug; the message says as much.

6. **Helper purity.** `dagger.mod._entrypoint` contains zero type
   introspection. Enforced by a grep test on forbidden patterns.

### Error handling

- Missing `_dagger_main.py` at runtime → actionable error from
  `template/runtime.py` ("run `dagger develop`").
- Syntax error in generated file → pathological; codegen-time test
  `ast.parse()`s the output before writing.
- Analyzer errors at codegen time → surfaced by the existing
  `AnalysisError` / `ValidationError` paths from spec 1.
- Dispatch miss → `case _:` raises with diagnostic.
- User-code exception in a `case` arm → propagates through
  `invoke()` → existing `fn_call.return_error(...)`.

## Non-goals

- **Removing dead code** in `dagger.mod._module`, `_resolver`,
  `_converter`. They become unreachable from the runtime template
  but remain in the package until a follow-up cleanup.
- **`legacyCodegenAtRuntime` opt-in for Python** — spec 3.
- **Committing `_dagger_main.py` by default** — spec 3.
- **Shared entrypoint helpers that do runtime type introspection** —
  violates invariant #6; disallowed.
- **Supporting dynamic `@function` registration** (decorators applied
  at runtime after import) — static codegen snapshot; matches Go.
- **Performance re-optimization via prebuilt `.dagger-build/`
  bundles** — spec 3 reclaims this via the commit-and-skip path.

## Testing

### Unit — Python

New `sdk/python/tests/mod/test_entrypoint_gen.py`:

- A simple module (one class, one function, one primitive arg) →
  assert the generated file parses with `ast.parse()`, contains a
  `build_module` async function and `invoke` async function, and the
  `invoke` body has a `match` with the expected `case` branches.
- Module with a Dagger object return type → assert the generated
  `unstructure_id` call is emitted.
- Module with a Dagger object parameter → assert the
  `coerce_id(dag.<type>_from_id, ...)` call is emitted.
- Module with an enum → assert `coerce_enum` / `unstructure_enum`
  calls with the right class references.
- Module with a list parameter → assert `coerce_list(element_coercer, ...)`
  with the right nested coercer.
- Module with an optional parameter / default value → assert the
  `inputs.get("name", default)` pattern is emitted, and the default
  value is rendered correctly.
- Module with multiple objects → assert each gets its own
  `_dag.type_def().with_object(...)` in `build_module` and its own
  `case` arms in `invoke`.
- Private attribute exclusion → assert the private field is absent
  from both outputs.

New `sdk/python/tests/mod/test_entrypoint_helpers.py`:

- Each helper function in `dagger.mod._entrypoint`:
  - `rehydrate_parent({}, Foo)` → `Foo()` with defaults
  - `rehydrate_parent({"x": 1}, Foo)` → `Foo(x=1)`
  - `coerce_enum(Color, "red")` → `Color.RED`
  - `coerce_list(int, ["1", "2"])` → `[1, 2]`
  - `unstructure_dataclass(Foo(x=1))` → `{"x": 1}`
  - `unstructure_enum(Color.RED)` → `"red"`
- **Purity guard**: grep the module source for forbidden patterns
  (`__annotations__`, `get_type_hints`, `cattrs`, `typing.get_args`,
  `typing.get_origin`) and assert none present.

### Integration — Go

New `core/integration/module_python_static_entrypoint_test.go`:

- `TestPythonStaticEntrypointBasic` — module with one simple function
  end-to-end; `dagger call` succeeds.
- `TestPythonStaticEntrypointCustomObject` — module with a custom
  `@object_type` class; a function returns it; a subsequent method
  call on that returned object succeeds (exercises parent-state
  rehydration and the match arm for the custom type).
- `TestPythonStaticEntrypointEnum` — module with an `@enum_type`;
  function takes it as arg, returns it.
- `TestPythonStaticEntrypointRegisterNoUserDeps` — module with a
  deliberately broken import; `--register` still succeeds (typedefs
  registration doesn't require user deps).
- `TestPythonStaticEntrypointFileShape` — invokes `dagger develop`,
  reads the generated `_dagger_main.py`, asserts top-level structure
  (has `build_module`, has `invoke` with a match statement, has no
  `__annotations__` references).

All existing Python integration tests (`TestSelfCalls/python`,
`TestSelfCallsOffPython`, the broad Python module suite) must
continue to pass — no regressions.

## Rollout

Spec 2 lands as five stg patches on top of the current 25-patch
stack:

1. `sdk(python): add _analyzer entrypoint CLI + entrypoint_gen`
   New `entrypoint_gen.py`; adds `entrypoint` subcommand to
   `python -m dagger.mod._analyzer`; extends `emit` subcommand with
   `--metadata-output` and `--schematool-output` so one CLI run
   serves both phase-0 outputs; unit tests for `entrypoint_gen`.

2. `sdk(python): add dagger.mod._entrypoint helper module`
   Hand-written coercion / rehydration / unstructure helpers; unit
   tests for each; purity guard test.

3. `sdk(python/runtime): wire entrypoint-gen as phase 3 of WithSDK`
   `sdk/python/runtime/main.go::WithSDK` gains the phase 3 exec;
   `template/runtime.py` rewritten to delegate to
   `<pkg>._dagger_main`; the prebuilt `.dagger-build/gen.py` and
   runtime fast-path go away (reclaimed by spec 3).

4. `sdk(python): decorators become markers`
   `@object_type` / `@function` / `@field` / `@enum_type` /
   `@interface` drop their runtime-registration side effects. The
   `__dagger_module__` build path is disabled but left in place with
   a deprecation comment.

5. `core/integration: python static entrypoint tests`
   Five new integration tests covering the invariants listed in the
   Testing section.

Each patch compiles (`go build` where relevant; `pytest` for Python)
and has focused tests before the next patch lands. Commits use `stg
new` with `Signed-off-by: Yves Brissaud <yves@dagger.io>`, no
`Co-Authored-By`.

## Spec 3 preview (not in this spec)

Once spec 2 lands:

- `_dagger_main.py` joins `gen.py` + `internal/dagger/**` in the
  set of committable generated files.
- `dagger init --sdk=python` writes
  `codegen.{legacyCodegenAtRuntime,automaticGitignore}` in
  `dagger.json`.
- `pythonSDK.Codegen()` short-circuits when `legacyCodegenAtRuntime
  == false` (mirror of Go PR 2).
- `pythonSDK.Runtime()` bypasses `WithSDK`'s codegen execs entirely
  when opted-in — goes straight from the committed files to
  `uv sync` + runtime.
- `sdk/python/runtime/` itself opts in and commits its own
  `_dagger_main.py` + `gen.py`.
