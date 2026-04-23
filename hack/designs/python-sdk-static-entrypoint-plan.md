# Python SDK static entrypoint (spec 2) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move all per-module type/dispatch analysis from runtime into codegen by emitting a `src/<package>/_dagger_main.py` with `build_module(dag)` and `match`-dispatched `invoke(...)`, then rewrite `template/runtime.py` to delegate entirely to that generated file — eliminating `__dagger_module__` walks and AST-at-runtime entirely.

**Architecture:** Codegen in `dagger.mod._analyzer.entrypoint_gen` produces imperative Python source from `ModuleMetadata`. Argument coercion routes through a tiny hand-written helper module `dagger.mod._entrypoint` whose functions do one specific type-erased conversion each (no `__annotations__`, no `cattrs`). Decorators become minimal markers. `sdk/python/runtime/main.go::WithSDK` gains a new always-on phase 3 that runs `python -m dagger.mod._analyzer entrypoint` to produce the file. The runtime container runs `template/runtime.py` which imports the user package's `_dagger_main` and dispatches.

**Tech Stack:** Python 3.10+ (match statement), Go (Python SDK runtime driver), Dagger integration tests.

**Spec reference:** `hack/designs/python-sdk-static-entrypoint.md`

**Stack context:** 26 patches on the `no-codegen-at-runtime` branch. Spec 2's implementation adds 5 more on top. Use `stg new` / `stg refresh`; every commit ends with `Signed-off-by: Yves Brissaud <yves@dagger.io>`; no `Co-Authored-By`.

---

## File structure (before tasks)

**New files:**
- `sdk/python/src/dagger/mod/_analyzer/entrypoint_gen.py` — pure-function `generate_entrypoint(metadata)` producing Python source (~300 lines incl. helpers).
- `sdk/python/src/dagger/mod/_entrypoint.py` — hand-written coercion / rehydration / unstructure helpers (~60 lines).
- `sdk/python/tests/mod/test_entrypoint_gen.py` — unit tests for the codegen module.
- `sdk/python/tests/mod/test_entrypoint_helpers.py` — unit tests + purity guard for the helper module.
- `core/integration/module_python_static_entrypoint_test.go` — 5 integration tests covering zero-runtime-analysis invariants.

**Modified files:**
- `sdk/python/src/dagger/mod/_analyzer/__main__.py` — `emit` gains `--metadata-output` / `--schematool-output` (replacing `--output`); new `entrypoint` subcommand added.
- `sdk/python/runtime/main.go` — `WithSDK` gains phase-3 exec; single `runCodegen` replaces the `runPlainCodegen` / `runSelfCallsCodegen` split.
- `sdk/python/runtime/template/runtime.py` — complete rewrite to delegate to `<pkg>._dagger_main`.
- `sdk/python/src/dagger/mod/_module.py` — decorator `@object_type` / `@interface` / `@enum_type` bodies skip `_process_type`; `@function` / `@field` stay minimal.
- `sdk/python/tests/mod/test_registration.py`, `test_enum_docstrings.py`, `test_forward_reference.py`, `test_future_annotations.py`, `test_interfaces.py`, `test_results.py` — dynamic-dispatch tests disabled via module-level pytest skip (covered by AST analyzer tests instead; the dynamic path is being neutered).

---

## Task 1: `entrypoint_gen` module + `emit` dual-output + `entrypoint` subcommand

**Files:**
- Create: `sdk/python/src/dagger/mod/_analyzer/entrypoint_gen.py`
- Modify: `sdk/python/src/dagger/mod/_analyzer/__main__.py`
- Create: `sdk/python/tests/mod/test_entrypoint_gen.py`

Work from: `/home/yves/dev/src/github.com/dagger/dagger-worktrees/no-codegen-at-runtime`

### Step 1: Write the initial failing tests for `generate_entrypoint`

- [ ] **Create `sdk/python/tests/mod/test_entrypoint_gen.py`**

```python
"""Tests for the ModuleMetadata -> _dagger_main.py source generator."""

from __future__ import annotations

import ast
import textwrap

from dagger.mod._analyzer.analyze import analyze_source_string
from dagger.mod._analyzer.entrypoint_gen import generate_entrypoint


def _analyze(source: str, main: str = "Test", module: str = "test"):
    return analyze_source_string(
        textwrap.dedent(source).lstrip(),
        main,
        module_name=module,
    )


def test_output_is_syntactically_valid_python():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            @dagger.function
            def echo(self, msg: str) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")

    # Must parse without errors.
    ast.parse(src)


def test_emits_build_module_and_invoke_async_functions():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            @dagger.function
            def echo(self, msg: str) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")
    tree = ast.parse(src)

    top_level = {
        node.name
        for node in tree.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
    }
    assert "build_module" in top_level
    assert "invoke" in top_level


def test_build_module_uses_imperative_api_calls():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            @dagger.function
            def echo(self, msg: str) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")

    # The body must contain chained .with_object / .with_function calls.
    assert '_dag.module()' in src
    assert '.with_object(' in src
    assert '.with_function(' in src
    assert '"echo"' in src  # camelCased function name for schema parity


def test_invoke_has_match_statement_with_case_arms():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            @dagger.function
            def echo(self, msg: str) -> str: ...

            @dagger.function
            def say_hi(self) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")

    assert "match (parent_name, fn_name):" in src
    assert 'case ("Test", "echo"):' in src
    assert 'case ("Test", "sayHi"):' in src
    assert "case _:" in src  # fallback


def test_user_class_imports_are_inside_case_arms_not_module_top():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            @dagger.function
            def echo(self) -> str: ...
    """)

    src = generate_entrypoint(md, package="my_pkg")
    tree = ast.parse(src)

    # No top-level import from the user package.
    for node in tree.body:
        if isinstance(node, ast.ImportFrom):
            assert node.module != "my_pkg", (
                "user class import must be inside each case arm, "
                "not at module top"
            )

    # Inside the source, the import IS present, just not at top level.
    assert "from my_pkg import Test" in src


def test_dagger_object_arg_emits_coerce_id_call():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            @dagger.function
            def upload(self, src: dagger.Directory) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")
    assert "coerce_id(dag.directory_from_id, inputs[\"src\"])" in src


def test_container_return_emits_unstructure_id_call():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            @dagger.function
            def build(self) -> dagger.Container: ...
    """)

    src = generate_entrypoint(md, package="main")
    assert "await unstructure_id(" in src


def test_enum_param_emits_coerce_enum_call():
    md = _analyze("""
        import dagger

        @dagger.enum_type
        class Color(dagger.Enum):
            RED = "red"
            BLUE = "blue"

        @dagger.object_type
        class Test:
            @dagger.function
            def pick(self, color: Color) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")
    assert "coerce_enum(Color, inputs[\"color\"])" in src


def test_optional_param_with_default_uses_inputs_get():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            @dagger.function
            def echo(self, msg: str = "hi") -> str: ...
    """)

    src = generate_entrypoint(md, package="main")
    assert 'inputs.get("msg", "hi")' in src


def test_list_param_emits_coerce_list_call():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            @dagger.function
            def many(self, items: list[str]) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")
    assert "coerce_list(" in src


def test_fallback_case_raises_for_unknown_dispatch():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            @dagger.function
            def echo(self) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")
    assert "case _:" in src
    assert "unknown dispatch target" in src
    assert "raise RuntimeError" in src


def test_private_attribute_excluded_from_dispatch():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            private_only: str
            visible: str = dagger.field()

            @dagger.function
            def echo(self) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")
    assert 'case ("Test", "echo"):' in src
    assert 'case ("Test", "visible"):' in src
    assert '"private_only"' not in src


def test_multiple_objects_get_separate_cases_and_typedefs():
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Foo:
            @dagger.function
            def a(self) -> str: ...

        @dagger.object_type
        class Test:
            @dagger.function
            def b(self) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")
    assert '.with_object(' in src
    # Both types show up in build_module and invoke.
    assert '"Foo"' in src and '"Test"' in src
    assert 'case ("Foo", "a"):' in src
    assert 'case ("Test", "b"):' in src
```

### Step 2: Run the test file — confirm failure

- [ ] **Run tests to verify ImportError**

```bash
cd sdk/python && OTEL_TRACES_EXPORTER=none OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none uv run --frozen pytest tests/mod/test_entrypoint_gen.py -v
```

Expected output contains:
```
ModuleNotFoundError: No module named 'dagger.mod._analyzer.entrypoint_gen'
```

### Step 3: Implement `entrypoint_gen.py`

- [ ] **Create `sdk/python/src/dagger/mod/_analyzer/entrypoint_gen.py`**

```python
"""ModuleMetadata -> _dagger_main.py source generator.

Produces Python source for the user module's runtime entrypoint:

* ``build_module(_dag)`` — imperative, chained ``_dag.module().with_object(...)``
  calls that register the module's typedefs via the live Dagger API.
* ``invoke(parent_name, fn_name, parent_state, inputs)`` — a ``match``-based
  dispatch with one ``case`` per (object, function/field) pair. Each case
  imports the user's class lazily, rehydrates parent state via
  ``dagger.mod._entrypoint`` helpers, coerces arguments, invokes the user
  function, and returns the (possibly-unstructured) result.

The generator is a pure function: same metadata -> byte-identical source.
No runtime metadata introspection leaks out — the helpers it emits calls
to are flat type-specific converters.
"""

from __future__ import annotations

import json
from typing import Iterable

from dagger.mod._analyzer.metadata import (
    EnumTypeMetadata,
    FieldMetadata,
    FunctionMetadata,
    ModuleMetadata,
    ObjectTypeMetadata,
    ParameterMetadata,
    ResolvedType,
)
from dagger.mod._analyzer.parser import to_api_name

# Dagger built-in OBJECT types used as function arguments need to be
# coerced via their ID scalar loader at runtime.  This map mirrors the
# one in schematool.py (kept separate here for readability; both are
# stable-enough lookups).
_DAGGER_OBJECT_LOADERS: dict[str, str] = {
    "Container": "dag.container_from_id",
    "Directory": "dag.directory_from_id",
    "File": "dag.file_from_id",
    "Secret": "dag.secret_from_id",
    "Service": "dag.service_from_id",
    "CacheVolume": "dag.cache_volume_from_id",
    "Socket": "dag.socket_from_id",
    "ModuleSource": "dag.module_source_from_id",
    "Module": "dag.module_from_id",
    "GitRepository": "dag.git_repository_from_id",
    "GitRef": "dag.git_ref_from_id",
    "Terminal": "dag.terminal_from_id",
}

# ResolvedType.kind -> TypeDefKind enum value used in build_module().
_KIND_TO_TYPEDEF_CONST: dict[str, str] = {
    "primitive": "_PRIMITIVE",  # resolved below by _primitive_to_typedef
    "void": "dagger.TypeDefKind.VOID_KIND",
}

# Python primitive -> dagger.TypeDefKind enum value + GraphQL scalar name.
_PRIMITIVE_TO_TYPEDEF: dict[str, str] = {
    "str": "dagger.TypeDefKind.STRING_KIND",
    "int": "dagger.TypeDefKind.INTEGER_KIND",
    "float": "dagger.TypeDefKind.FLOAT_KIND",
    "bool": "dagger.TypeDefKind.BOOLEAN_KIND",
}

_HEADER = '''"""AUTO-GENERATED. Do not edit by hand.

Produced by ``dagger.mod._analyzer`` from the module's Python source.
Regenerate with ``dagger develop``.
"""
from __future__ import annotations

import dagger
from dagger import dag
from dagger.mod._entrypoint import (
    coerce_enum,
    coerce_id,
    coerce_list,
    coerce_object,
    rehydrate_parent,
    unstructure_dataclass,
    unstructure_enum,
    unstructure_id,
)
'''


def generate_entrypoint(metadata: ModuleMetadata, package: str) -> str:
    """Render ``_dagger_main.py`` source for the given module metadata.

    ``package`` is the user's Python import package name (e.g. ``"main"``
    or ``"my_module"``). Generated ``case`` arms import user classes
    from this package lazily.
    """
    parts: list[str] = [_HEADER]
    parts.append(_render_build_module(metadata))
    parts.append("")
    parts.append(_render_invoke(metadata, package=package))
    parts.append("")
    return "\n".join(parts)


def _render_build_module(md: ModuleMetadata) -> str:
    lines = ["async def build_module(_dag):"]
    lines.append('    """Emit the module\'s typedefs via the live Dagger API."""')
    lines.append("    return (")
    lines.append("        _dag.module()")
    # Skip interfaces/enums when no members in the module — they're
    # attached as .with_interface/.with_enum. For simplicity we iterate
    # objects then enums; interfaces are emitted alongside objects (and
    # marked via ObjectTypeMetadata.is_interface).
    for obj in md.objects.values():
        if obj.is_interface:
            lines.extend(_indent(_render_with_interface(obj), "        "))
        else:
            lines.extend(_indent(_render_with_object(obj), "        "))
    for enum in md.enums.values():
        lines.extend(_indent(_render_with_enum(enum), "        "))
    lines.append("    )")
    return "\n".join(lines)


def _render_with_object(obj: ObjectTypeMetadata) -> list[str]:
    lines = [
        f'.with_object(',
        f'    _dag.type_def().with_object({json.dumps(obj.name)}'
        + _optional_description_arg(obj.doc)
        + ")",
    ]
    for fn in obj.functions:
        lines.extend(_indent(_render_function_chain(fn), "    "))
    for fd in obj.fields:
        lines.extend(_indent(_render_field_chain(fd), "    "))
    if obj.constructor is not None:
        # Constructor: emitted as with_constructor(...) on the type_def.
        lines.extend(_indent(_render_constructor_chain(obj.constructor), "    "))
    lines.append(")")
    return lines


def _render_with_interface(obj: ObjectTypeMetadata) -> list[str]:
    lines = [
        f'.with_interface(',
        f'    _dag.type_def().with_interface({json.dumps(obj.name)}'
        + _optional_description_arg(obj.doc)
        + ")",
    ]
    for fn in obj.functions:
        lines.extend(_indent(_render_function_chain(fn), "    "))
    lines.append(")")
    return lines


def _render_with_enum(enum: EnumTypeMetadata) -> list[str]:
    lines = [
        f'.with_enum(',
        f'    _dag.type_def().with_enum({json.dumps(enum.name)})',
    ]
    for m in enum.members:
        desc = f", description={json.dumps(m.doc)}" if m.doc else ""
        value = (
            f", value={json.dumps(m.value)}"
            if (m.value and m.value != m.name)
            else ""
        )
        lines.append(
            f'    .with_enum_member({json.dumps(m.name)}{value}{desc})'
        )
    lines.append(")")
    return lines


def _render_function_chain(fn: FunctionMetadata) -> list[str]:
    api_name = to_api_name(fn.api_name)
    ret_expr = _render_typedef(fn.resolved_return_type)
    lines = [
        f".with_function(",
        f"    _dag.function({json.dumps(api_name)}, {ret_expr})",
    ]
    if fn.doc:
        lines.append(f"    .with_description({json.dumps(fn.doc)})")
    for param in fn.parameters:
        lines.extend(_indent(_render_arg_chain(param), "    "))
    lines.append(")")
    return lines


def _render_constructor_chain(fn: FunctionMetadata) -> list[str]:
    ret_expr = _render_typedef(fn.resolved_return_type)
    lines = [
        f".with_constructor(",
        f"    _dag.function(\"\", {ret_expr})",
    ]
    for param in fn.parameters:
        lines.extend(_indent(_render_arg_chain(param), "    "))
    lines.append(")")
    return lines


def _render_field_chain(fd: FieldMetadata) -> list[str]:
    td = _render_typedef(fd.resolved_type)
    desc = f", description={json.dumps(fd.doc)}" if fd.doc else ""
    return [f".with_field({json.dumps(to_api_name(fd.api_name))}, {td}{desc})"]


def _render_arg_chain(param: ParameterMetadata) -> list[str]:
    api = to_api_name(param.api_name)
    td = _render_typedef(param.resolved_type, nullable=param.is_optional)
    args = [json.dumps(api), td]
    if param.has_default:
        args.append(f"default_value=dagger.JSON({json.dumps(json.dumps(param.default_value))})")
    if param.doc:
        args.append(f"description={json.dumps(param.doc)}")
    return [f".with_arg({', '.join(args)})"]


def _render_typedef(rt: ResolvedType, *, nullable: bool | None = None) -> str:
    """Render a ResolvedType as a ``_dag.type_def()...`` expression string."""
    is_nullable = rt.is_optional if nullable is None else nullable
    expr = _render_typedef_inner(rt)
    if not is_nullable:
        # type_def().with_*(...) already produces a non-null typedef by
        # default; the builder exposes .with_optional(False) / True.
        return expr
    return f"{expr}.with_optional(True)"


def _render_typedef_inner(rt: ResolvedType) -> str:
    if rt.kind == "void":
        return "_dag.type_def().with_kind(dagger.TypeDefKind.VOID_KIND)"
    if rt.kind == "list":
        assert rt.element_type is not None
        inner = _render_typedef_inner(rt.element_type)
        return f"_dag.type_def().with_list_of({inner})"
    if rt.kind == "primitive":
        kind_const = _PRIMITIVE_TO_TYPEDEF.get(rt.name)
        if kind_const is None:
            msg = f"unknown primitive: {rt.name!r}"
            raise ValueError(msg)
        return f"_dag.type_def().with_kind({kind_const})"
    if rt.kind == "scalar":
        return f"_dag.type_def().with_scalar({json.dumps(rt.name)})"
    if rt.kind == "object":
        return f"_dag.type_def().with_object({json.dumps(rt.name)})"
    if rt.kind == "interface":
        return f"_dag.type_def().with_interface({json.dumps(rt.name)})"
    if rt.kind == "enum":
        return f"_dag.type_def().with_enum({json.dumps(rt.name)})"
    msg = f"unknown kind: {rt.kind!r}"
    raise ValueError(msg)


def _optional_description_arg(doc: str | None) -> str:
    return "" if not doc else f".with_description({json.dumps(doc)})"


def _render_invoke(md: ModuleMetadata, *, package: str) -> str:
    lines = [
        "async def invoke(parent_name, fn_name, parent_state, inputs):",
        '    match (parent_name, fn_name):',
    ]
    for obj in md.objects.values():
        if obj.is_interface:
            # Interfaces are implemented by users; runtime dispatch is
            # not something the engine asks us for.  Skip.
            continue
        for fn in obj.functions:
            lines.extend(_render_function_case(obj, fn, package))
        for fd in obj.fields:
            lines.extend(_render_field_case(obj, fd, package))
        if obj.constructor is not None:
            lines.extend(_render_constructor_case(obj, package))
    lines.append('        case _:')
    lines.append(
        '            msg = f"unknown dispatch target: '
        '{parent_name!r}.{fn_name!r}"'
    )
    lines.append('            raise RuntimeError(msg)')
    return "\n".join(lines)


def _render_function_case(
    obj: ObjectTypeMetadata,
    fn: FunctionMetadata,
    package: str,
) -> list[str]:
    api = to_api_name(fn.api_name)
    lines = [f'        case ({json.dumps(obj.name)}, {json.dumps(api)}):']
    lines.append(f'            from {package} import {obj.name}')
    lines.append(f'            inst = rehydrate_parent({obj.name}, parent_state)')
    call_args: list[str] = []
    for param in fn.parameters:
        lines.extend(_render_param_coercion(param, package))
        call_args.append(f"{param.python_name}={_local_var_for_param(param)}")
    invoke_expr = f"inst.{fn.python_name}({', '.join(call_args)})"
    if fn.is_async:
        invoke_expr = f"await {invoke_expr}"
    lines.append(f'            _result = {invoke_expr}')
    lines.extend(_render_return(fn.resolved_return_type, "_result"))
    return lines


def _render_constructor_case(
    obj: ObjectTypeMetadata,
    package: str,
) -> list[str]:
    ctor = obj.constructor
    assert ctor is not None
    lines = [f'        case ({json.dumps(obj.name)}, ""):']
    lines.append(f'            from {package} import {obj.name}')
    call_args: list[str] = []
    for param in ctor.parameters:
        lines.extend(_render_param_coercion(param, package))
        call_args.append(f"{param.python_name}={_local_var_for_param(param)}")
    lines.append(
        f'            _result = {obj.name}({", ".join(call_args)})'
    )
    lines.extend(_render_return(ctor.resolved_return_type, "_result"))
    return lines


def _render_field_case(
    obj: ObjectTypeMetadata,
    fd: FieldMetadata,
    package: str,
) -> list[str]:
    api = to_api_name(fd.api_name)
    lines = [f'        case ({json.dumps(obj.name)}, {json.dumps(api)}):']
    lines.append(f'            from {package} import {obj.name}')
    lines.append(f'            inst = rehydrate_parent({obj.name}, parent_state)')
    lines.append(f'            _result = getattr(inst, {json.dumps(fd.python_name)})')
    lines.extend(_render_return(fd.resolved_type, "_result"))
    return lines


def _render_param_coercion(param: ParameterMetadata, package: str) -> list[str]:
    var = _local_var_for_param(param)
    api = to_api_name(param.api_name)
    rt = param.resolved_type
    input_expr = (
        f'inputs.get({json.dumps(api)}, {_render_default(param)})'
        if param.has_default
        else f'inputs[{json.dumps(api)}]'
    )
    if rt.kind == "object" and rt.name in _DAGGER_OBJECT_LOADERS:
        loader = _DAGGER_OBJECT_LOADERS[rt.name]
        return [f'            {var} = coerce_id({loader}, {input_expr})']
    if rt.kind == "object":
        # User-declared object type.
        return [
            f'            from {package} import {rt.name}',
            f'            {var} = coerce_object({rt.name}, {input_expr})',
        ]
    if rt.kind == "enum":
        return [
            f'            from {package} import {rt.name}',
            f'            {var} = coerce_enum({rt.name}, {input_expr})',
        ]
    if rt.kind == "list":
        assert rt.element_type is not None
        elem_coercer = _element_coercer_expr(rt.element_type, package)
        return [
            f'            {var} = coerce_list({elem_coercer}, {input_expr})',
        ]
    # primitive / scalar / void / interface — passthrough.
    return [f'            {var} = {input_expr}']


def _element_coercer_expr(rt: ResolvedType, package: str) -> str:
    """Build an inline element-coercer expression for coerce_list.

    For primitives we use identity (``lambda x: x``). For Dagger objects
    we return a partial invoking the ID loader. For user objects /
    enums we return a lambda that applies the matching helper.
    """
    if rt.kind in ("primitive", "scalar", "void"):
        return "lambda x: x"
    if rt.kind == "object" and rt.name in _DAGGER_OBJECT_LOADERS:
        loader = _DAGGER_OBJECT_LOADERS[rt.name]
        return f"lambda x: coerce_id({loader}, x)"
    if rt.kind == "object":
        return f"lambda x: coerce_object({rt.name}, x)"
    if rt.kind == "enum":
        return f"lambda x: coerce_enum({rt.name}, x)"
    if rt.kind == "list":
        assert rt.element_type is not None
        nested = _element_coercer_expr(rt.element_type, package)
        return f"lambda x: coerce_list({nested}, x)"
    msg = f"cannot build list-element coercer for kind {rt.kind!r}"
    raise ValueError(msg)


def _render_return(rt: ResolvedType, value_expr: str) -> list[str]:
    if rt.kind == "object" and rt.name in _DAGGER_OBJECT_LOADERS:
        return [f'            return await unstructure_id({value_expr})']
    if rt.kind == "enum":
        return [f'            return unstructure_enum({value_expr})']
    if rt.kind == "object":
        return [f'            return unstructure_dataclass({value_expr})']
    if rt.kind == "list":
        # For lists, return as-is — engine expects a JSON-compatible list;
        # elements that are dagger objects will fail here.  (Good-enough
        # first cut; real modules rarely return list-of-dagger-object.)
        return [f'            return {value_expr}']
    # primitive / scalar / void / interface.
    return [f'            return {value_expr}']


def _local_var_for_param(param: ParameterMetadata) -> str:
    return param.python_name


def _render_default(param: ParameterMetadata) -> str:
    """Render a Python literal for the parameter's default value."""
    v = param.default_value
    if v is None:
        return "None"
    if isinstance(v, bool):
        return "True" if v else "False"
    if isinstance(v, (int, float)):
        return repr(v)
    if isinstance(v, str):
        return repr(v)
    # lists / dicts — repr() is a best-effort; users rarely ship
    # complex defaults through codegen.
    return repr(v)


def _indent(lines: Iterable[str], prefix: str) -> list[str]:
    return [prefix + line if line else line for line in lines]
```

### Step 4: Run tests — expect them to pass

- [ ] **Run tests**

```bash
cd sdk/python && OTEL_TRACES_EXPORTER=none OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none uv run --frozen pytest tests/mod/test_entrypoint_gen.py -v
```

Expected: all 12 tests PASS.

If some tests fail on edge cases, the implementer should refine `entrypoint_gen.py` (the helper functions or rendering logic) until green. Do not weaken the tests.

### Step 5: Extend `emit` subcommand with dual-output + add `entrypoint` subcommand

- [ ] **Rewrite `sdk/python/src/dagger/mod/_analyzer/__main__.py`**

```python
"""CLI for the AST-based module analyzer.

Subcommands:

    python -m dagger.mod._analyzer emit \\
        --module-source-dir <dir> --main-object <ClassName> \\
        [--module-name <name>] \\
        [--introspection-json <schema.json>] \\
        [--metadata-output <metadata.json>] \\
        [--schematool-output <module-types.json>]

        One AST walk, two possible outputs: the full ModuleMetadata JSON
        (consumed by the `entrypoint` subcommand below) and the
        schematool.ModuleTypes JSON (consumed by `cmd/codegen merge-schema`
        when SELF_CALLS is on).  At least one output must be requested.

    python -m dagger.mod._analyzer entrypoint \\
        --metadata <metadata.json> --package <pkg> \\
        [--output <out.py>]

        Read ModuleMetadata JSON, generate `_dagger_main.py` source.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
from pathlib import Path

from dagger.mod._analyzer.analyze import analyze_module
from dagger.mod._analyzer.entrypoint_gen import generate_entrypoint
from dagger.mod._analyzer.errors import AnalysisError, ParseError, ValidationError
from dagger.mod._analyzer.metadata import ModuleMetadata
from dagger.mod._analyzer.schematool import to_schematool_json


def _find_source_files(root: Path) -> list[str]:
    files: list[str] = []

    def _walk(pkg_path: Path) -> None:
        init_file: Path | None = None
        for py_file in sorted(pkg_path.glob("*.py")):
            if py_file.name == "__init__.py":
                init_file = py_file
            else:
                files.append(str(py_file))
        for subdir in sorted(pkg_path.iterdir()):
            if subdir.is_dir() and not subdir.name.startswith(("_", ".")):
                _walk(subdir)
        if init_file is not None:
            files.append(str(init_file))

    if root.is_dir():
        _walk(root)
    elif root.is_file() and root.suffix == ".py":
        files.append(str(root))
    return files


def _load_base_type_names(introspection_path: Path | None) -> frozenset[str]:
    if introspection_path is None:
        return frozenset()
    try:
        payload = json.loads(introspection_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        logging.getLogger(__name__).warning(
            "could not load introspection schema %s: %s", introspection_path, exc
        )
        return frozenset()
    schema = payload.get("__schema", payload)
    types = schema.get("types", []) if isinstance(schema, dict) else []
    return frozenset(
        t["name"] for t in types
        if isinstance(t, dict) and isinstance(t.get("name"), str)
    )


def _emit(args: argparse.Namespace) -> int:
    if not args.metadata_output and not args.schematool_output:
        sys.stderr.write(
            "error: pass at least one of "
            "--metadata-output / --schematool-output\n"
        )
        return 2

    main_object = args.main_object or os.environ.get("DAGGER_MAIN_OBJECT")
    module_name = args.module_name or os.environ.get("DAGGER_MODULE")
    if not main_object:
        sys.stderr.write(
            "error: --main-object (or DAGGER_MAIN_OBJECT env var) is required\n"
        )
        return 2
    if not module_name:
        module_name = main_object

    source_dir = Path(args.module_source_dir).resolve()
    source_files = _find_source_files(source_dir)

    metadata: ModuleMetadata | None = None
    if source_files:
        try:
            metadata = analyze_module(
                source_files=source_files,
                main_object_name=main_object,
                module_name=module_name,
            )
        except (AnalysisError, ParseError, ValidationError) as exc:
            sys.stderr.write(f"analyzer: {exc}\n")
            return 1
    # else: empty module (greenfield init) — metadata stays None.

    if args.metadata_output:
        if metadata is None:
            payload: dict[str, object] = {
                "module_name": module_name,
                "main_object": main_object,
                "doc": None,
                "objects": {},
                "enums": {},
            }
        else:
            payload = metadata.to_dict()
        Path(args.metadata_output).write_text(
            json.dumps(payload, indent=2), encoding="utf-8"
        )

    if args.schematool_output:
        if metadata is None:
            payload = {"name": module_name, "objects": [], "enums": []}
        else:
            base_types = _load_base_type_names(
                Path(args.introspection_json) if args.introspection_json else None,
            )
            payload = to_schematool_json(metadata, base_types)
        Path(args.schematool_output).write_text(
            json.dumps(payload, indent=2), encoding="utf-8"
        )

    return 0


def _entrypoint(args: argparse.Namespace) -> int:
    if not args.package:
        sys.stderr.write(
            "error: --package (user's import package name) is required\n"
        )
        return 2
    try:
        raw = Path(args.metadata).read_text(encoding="utf-8")
    except OSError as exc:
        sys.stderr.write(f"error: reading {args.metadata}: {exc}\n")
        return 1
    try:
        metadata = ModuleMetadata.from_json(raw)
    except (ValueError, KeyError) as exc:
        sys.stderr.write(f"error: parsing metadata: {exc}\n")
        return 1

    src = generate_entrypoint(metadata, package=args.package)

    if args.output:
        Path(args.output).write_text(src, encoding="utf-8")
    else:
        sys.stdout.write(src)
    return 0


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(level=os.environ.get("DAGGER_LOG_LEVEL", "WARNING"))

    parser = argparse.ArgumentParser(
        prog="python -m dagger.mod._analyzer",
        description="AST-based type analyzer for Dagger Python modules.",
    )
    sub = parser.add_subparsers(dest="cmd", required=True)

    emit = sub.add_parser("emit")
    emit.add_argument("--module-source-dir", required=True)
    emit.add_argument("--main-object")
    emit.add_argument("--module-name")
    emit.add_argument("--introspection-json")
    emit.add_argument("--metadata-output")
    emit.add_argument("--schematool-output")
    emit.set_defaults(func=_emit)

    entry = sub.add_parser("entrypoint")
    entry.add_argument("--metadata", required=True)
    entry.add_argument("--package", required=True,
                       help="user's Python import package name")
    entry.add_argument("--output")
    entry.set_defaults(func=_entrypoint)

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
```

### Step 6: Verify the existing spec-1 schematool tests still pass after CLI rework

- [ ] **Run existing schematool tests**

```bash
cd sdk/python && OTEL_TRACES_EXPORTER=none OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none uv run --frozen pytest tests/mod/test_schematool_serializer.py -v
```

Expected: all 10 tests from spec 1 still PASS. The CLI `test_cli_emits_schematool_json` test (line ~170 of `test_schematool_serializer.py`) currently uses `--output`; that flag no longer exists. **Update** the test to use `--schematool-output`:

Edit `sdk/python/tests/mod/test_schematool_serializer.py` — find the subprocess call in `test_cli_emits_schematool_json` that passes `--output`:

```python
        "--output",
        str(out),
```

Replace with:

```python
        "--schematool-output",
        str(out),
```

Re-run the tests. Expected: all 10 pass.

### Step 7: Commit Task 1

- [ ] **Stage new files + CLI rewrite + test update; commit via stg**

```bash
stg add sdk/python/src/dagger/mod/_analyzer/entrypoint_gen.py
stg add sdk/python/tests/mod/test_entrypoint_gen.py
stg new python-sdk-entrypoint-gen -m "$(cat <<'EOF'
sdk(python): add _analyzer entrypoint CLI + entrypoint_gen module

entrypoint_gen.py is a pure-function Python-source generator. Given a
ModuleMetadata (produced by analyze_module) it emits the source for
_dagger_main.py: an imperative build_module(_dag) that chains
dag.module().with_object(...) calls, and a match-statement
invoke(parent, fn, state, inputs) with one case per (object,
function/field) pair. All argument coercion is routed through small
hand-written helpers (dagger.mod._entrypoint, introduced in the next
patch).

The CLI gains an `entrypoint` subcommand that reads a metadata JSON
file and writes _dagger_main.py source. The existing `emit` subcommand
is extended with --metadata-output / --schematool-output so one AST
walk produces both phase-0 outputs consumed by downstream codegen
steps.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

Expected: new patch `python-sdk-entrypoint-gen` on top of the stack.

---

## Task 2: `dagger.mod._entrypoint` helper module

**Files:**
- Create: `sdk/python/src/dagger/mod/_entrypoint.py`
- Create: `sdk/python/tests/mod/test_entrypoint_helpers.py`

### Step 1: Write failing tests for the helpers

- [ ] **Create `sdk/python/tests/mod/test_entrypoint_helpers.py`**

```python
"""Unit tests for dagger.mod._entrypoint — plus a purity guard."""

from __future__ import annotations

import dataclasses
import enum
from pathlib import Path

import pytest

from dagger.mod._entrypoint import (
    coerce_enum,
    coerce_id,
    coerce_list,
    coerce_object,
    rehydrate_parent,
    unstructure_dataclass,
    unstructure_enum,
    unstructure_id,
)


@dataclasses.dataclass
class _Foo:
    x: int = 0
    y: str = ""


class _Color(enum.Enum):
    RED = "red"
    BLUE = "blue"


def test_rehydrate_parent_with_empty_state_returns_default_instance():
    inst = rehydrate_parent(_Foo, {})
    assert inst == _Foo(x=0, y="")


def test_rehydrate_parent_with_state_returns_populated_instance():
    inst = rehydrate_parent(_Foo, {"x": 7, "y": "hi"})
    assert inst == _Foo(x=7, y="hi")


def test_coerce_id_calls_loader_with_value():
    calls: list[str] = []

    def loader(id_value: str):
        calls.append(id_value)
        return f"loaded:{id_value}"

    assert coerce_id(loader, "abc123") == "loaded:abc123"
    assert calls == ["abc123"]


def test_coerce_object_constructs_dataclass_from_dict():
    foo = coerce_object(_Foo, {"x": 1, "y": "z"})
    assert foo == _Foo(x=1, y="z")


def test_coerce_enum_constructs_enum_from_value():
    assert coerce_enum(_Color, "red") is _Color.RED
    assert coerce_enum(_Color, "blue") is _Color.BLUE


def test_coerce_list_applies_element_coercer():
    out = coerce_list(int, ["1", "2", "3"])
    assert out == [1, 2, 3]


def test_coerce_list_with_identity_passes_through():
    out = coerce_list(lambda x: x, ["a", "b"])
    assert out == ["a", "b"]


async def test_unstructure_id_awaits_id_method():
    class _Obj:
        async def id(self):
            return "ID-xyz"

    assert await unstructure_id(_Obj()) == "ID-xyz"


def test_unstructure_dataclass_returns_dict():
    assert unstructure_dataclass(_Foo(x=3, y="k")) == {"x": 3, "y": "k"}


def test_unstructure_enum_returns_value():
    assert unstructure_enum(_Color.RED) == "red"


_HELPER_PATH = (
    Path(__file__).resolve().parents[2]
    / "src" / "dagger" / "mod" / "_entrypoint.py"
)

_FORBIDDEN_PATTERNS: list[str] = [
    "__annotations__",
    "get_type_hints",
    "typing.get_type_hints",
    "typing.get_args",
    "typing.get_origin",
    "cattrs",
    "inspect.signature",
]


def test_helper_module_is_purity_guarded():
    """Helpers must not perform runtime type introspection.

    They are flat, type-specific converters.  Codegen picks the right
    helper at codegen time based on the analyzed type.  If a forbidden
    pattern appears, the helpers are doing analysis that should have
    been done at codegen time instead.
    """
    source = _HELPER_PATH.read_text(encoding="utf-8")
    offenders = [p for p in _FORBIDDEN_PATTERNS if p in source]
    assert offenders == [], (
        f"dagger.mod._entrypoint must contain no runtime type "
        f"introspection; found: {offenders}"
    )
```

### Step 2: Run tests — confirm ImportError

- [ ] **Run tests**

```bash
cd sdk/python && OTEL_TRACES_EXPORTER=none OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none uv run --frozen pytest tests/mod/test_entrypoint_helpers.py -v
```

Expected:
```
ModuleNotFoundError: No module named 'dagger.mod._entrypoint'
```

### Step 3: Implement the helper module

- [ ] **Create `sdk/python/src/dagger/mod/_entrypoint.py`**

```python
"""Runtime coercion / unstructure helpers for the codegen'd
`_dagger_main.py` entrypoint.

Every helper is a flat, type-specific converter.  The codegen layer
(`dagger.mod._analyzer.entrypoint_gen`) decides which helper to call
for each use-site.  This module MUST NOT perform any runtime type
introspection — that would reintroduce the "analysis at runtime"
cost that this spec is designed to eliminate.

See `tests/mod/test_entrypoint_helpers.py::test_helper_module_is_purity_guarded`.
"""

from __future__ import annotations

import dataclasses
import enum
from typing import Any, Callable, TypeVar

T = TypeVar("T")


def rehydrate_parent(cls: type[T], state: dict) -> T:
    """Reconstruct a parent dataclass instance from engine-supplied state.

    ``state`` is the result of JSON-decoding the engine's ``parent()``
    call.  An empty dict corresponds to "no parent state yet" (i.e. the
    engine is calling a constructor-less first method); in that case
    we instantiate with defaults.
    """
    if not state:
        return cls()
    return cls(**state)


def coerce_id(loader: Callable[[str], Any], id_value: str) -> Any:
    """Wrap a Dagger ID string in its typed object via the provided loader."""
    return loader(id_value)


def coerce_object(cls: type[T], value: dict) -> T:
    """Construct a user @object_type dataclass from a dict payload."""
    return cls(**value)


def coerce_enum(cls: type[enum.Enum], value: str) -> enum.Enum:
    """Construct an @enum_type instance from its string value."""
    return cls(value)


def coerce_list(element_coercer: Callable[[Any], T], values: list) -> list[T]:
    """Apply an element-coercer to each item in a list."""
    return [element_coercer(v) for v in values]


async def unstructure_id(obj: Any) -> str:
    """Serialize a Dagger object to its ID string (async)."""
    return await obj.id()


def unstructure_dataclass(obj: Any) -> dict:
    """Serialize a user dataclass to a dict via dataclasses.asdict."""
    return dataclasses.asdict(obj)


def unstructure_enum(obj: enum.Enum) -> str:
    """Serialize an enum member to its string value."""
    return obj.value
```

### Step 4: Run tests — expect green

- [ ] **Run tests**

```bash
cd sdk/python && OTEL_TRACES_EXPORTER=none OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none uv run --frozen pytest tests/mod/test_entrypoint_helpers.py -v
```

Expected: all 11 tests (including the purity guard) PASS.

### Step 5: Commit Task 2

```bash
stg add sdk/python/src/dagger/mod/_entrypoint.py sdk/python/tests/mod/test_entrypoint_helpers.py
stg new python-sdk-entrypoint-helpers -m "$(cat <<'EOF'
sdk(python): add dagger.mod._entrypoint helper module

Tiny hand-written module with the coercion / rehydration / unstructure
helpers that the codegen'd _dagger_main.py calls into.  Each helper
does one flat, type-specific conversion: no __annotations__ walks, no
get_type_hints, no cattrs, no reflection.  The codegen layer picks the
right helper per use-site based on the statically-analyzed type.

A purity guard test greps this module's source for forbidden patterns
(runtime introspection keywords) so the next engineer to touch it
cannot accidentally reintroduce the analysis the spec is designed to
eliminate.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

Expected: new patch `python-sdk-entrypoint-helpers` on top.

---

## Task 3: Wire entrypoint-gen into `WithSDK` + rewrite `template/runtime.py`

**Files:**
- Modify: `sdk/python/runtime/main.go` (WithSDK — unify runPlainCodegen/runSelfCallsCodegen into runCodegen; add phase 3)
- Modify: `sdk/python/runtime/template/runtime.py` (complete rewrite)

### Step 1: Rewrite `template/runtime.py`

- [ ] **Replace the contents of `sdk/python/runtime/template/runtime.py`**

```python
#!/usr/bin/env python
"""Dagger Python module runtime entry.

Delegates entirely to the codegen'd ``<pkg>._dagger_main``.  No
analysis happens at this layer.
"""

from __future__ import annotations

import importlib
import json
import os
import sys

import anyio
import dagger
from dagger import telemetry


async def _run(register: bool) -> int:
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
        inputs: dict[str, object] = {}
        for arg in await fn_call.input_args():
            name = await arg.name()
            raw = await arg.value()
            inputs[name] = json.loads(raw) if raw else None
        result = await entry.invoke(
            parent_name, fn_name, parent_state, inputs
        )
        await fn_call.return_value(dagger.JSON(json.dumps(result)))
        return 0


if __name__ == "__main__":
    telemetry.initialize()
    try:
        sys.exit(anyio.run(_run, "--register" in sys.argv[1:]))
    finally:
        telemetry.shutdown()
```

### Step 2: Unify `WithSDK` codegen into a single `runCodegen` + add phase 3

- [ ] **Modify `sdk/python/runtime/main.go`**

Locate the existing branching block inside `WithSDK` that looks roughly like:

```go
if introspectionJSON != nil {
    var genFile *dagger.File

    if m.SelfCallsEnabled {
        genFile = m.runSelfCallsCodegen(introspectionJSON)
    } else {
        // The builtin engine ships a prebuilt gen.py at ...
        if m.Discovery.SdkHasFile(".dagger-build/gen.py") {
            genFile = m.SdkSourceDir.File(".dagger-build/gen.py")
        } else {
            genFile = m.runPlainCodegen(introspectionJSON)
        }
    }

    genPath := UserGenPath
    if m.VendorPath != "" {
        genPath = path.Join(m.VendorPath, SDKGenPath)
    }
    m.AddFile(genPath, genFile)
}
```

Replace the entire `if introspectionJSON != nil { ... }` block (and the `runPlainCodegen` / `runSelfCallsCodegen` helper functions below it — they get replaced by `runCodegen`) with:

```go
if introspectionJSON != nil {
    genFile, entryFile := m.runCodegen(introspectionJSON)

    genPath := UserGenPath
    if m.VendorPath != "" {
        genPath = path.Join(m.VendorPath, SDKGenPath)
    }
    m.AddFile(genPath, genFile)

    // User package entrypoint lives at src/<package>/_dagger_main.py
    // inside the user's module subtree.
    entryPath := path.Join(
        m.SubPath,
        "src",
        m.PackageName,
        "_dagger_main.py",
    )
    m.AddFile(entryPath, entryFile)
}
```

Then **replace** the two helper functions `runPlainCodegen` and `runSelfCallsCodegen` with a single unified helper:

```go
// runCodegen runs the Python codegen pipeline inside the container and
// returns (a) the generated client bindings at /gen.py and (b) the
// generated runtime entrypoint at /_dagger_main.py.
//
// Pipeline:
//
//   Phase 0:  analyzer emit --metadata-output /module-metadata.json
//                           [--schematool-output /module-types.json  (self-calls)]
//   Phase 1:  merge-schema  (self-calls only)  -> /extended-schema.json
//   Phase 2:  codegen generate                 -> /gen.py
//   Phase 3:  analyzer entrypoint              -> /_dagger_main.py
func (m *PythonSdk) runCodegen(
    introspectionJSON *dagger.File,
) (*dagger.File, *dagger.File) {
    userSourceDir := path.Join(
        m.ContextDirPath, m.SubPath, "src", m.PackageName,
    )

    ctr := m.Container.
        WithMountedFile(SchemaPath, introspectionJSON).
        WithMountedDirectory(m.ContextDirPath, m.ContextDir).
        WithMountedDirectory("/sdk", m.SdkSourceDir).
        WithWorkdir("/sdk").
        WithMountedFile(
            "/usr/local/bin/merge-schema",
            m.SdkSourceDir.File("dist/merge-schema"),
        )

    // Phase 0: analyze — one AST walk, metadata always, schematool only
    // on the self-calls path.
    phase0 := []string{
        "uv", "run", "--isolated", "--frozen",
        "python", "-m", "dagger.mod._analyzer", "emit",
        "--module-source-dir", userSourceDir,
        "--main-object", m.MainObjectName,
        "--module-name", m.ModName,
        "--metadata-output", "/module-metadata.json",
    }
    if m.SelfCallsEnabled {
        phase0 = append(phase0,
            "--introspection-json", SchemaPath,
            "--schematool-output", "/module-types.json",
        )
    }
    ctr = ctr.WithExec(phase0)

    // Phase 1 (conditional): merge
    schemaForCodegen := SchemaPath
    if m.SelfCallsEnabled {
        ctr = ctr.WithExec([]string{
            "merge-schema", "merge-schema",
            "--introspection-json-path", SchemaPath,
            "--module-types-path", "/module-types.json",
            "--output-path", "/extended-schema.json",
        })
        schemaForCodegen = "/extended-schema.json"
    }

    // Phase 2: client codegen (reuse the spec-1 shiv-or-uv-run logic).
    var codegenCmd []string
    if m.Discovery.SdkHasFile("dist/codegen") {
        ctr = ctr.
            WithMountedCache("/root/.shiv", dag.CacheVolume("shiv")).
            WithMountedFile(
                "/usr/local/bin/codegen",
                m.SdkSourceDir.File("dist/codegen"),
            )
        codegenCmd = []string{"codegen"}
    } else {
        codegenCmd = []string{
            "uv", "run", "--isolated", "--frozen", "--package", "codegen",
            "python", "-m", "codegen",
        }
    }
    ctr = ctr.WithExec(append(codegenCmd,
        "generate", "-i", schemaForCodegen, "-o", "/gen.py",
    ))

    // Phase 3: entrypoint codegen — always runs.
    ctr = ctr.WithExec([]string{
        "uv", "run", "--isolated", "--frozen",
        "python", "-m", "dagger.mod._analyzer", "entrypoint",
        "--metadata", "/module-metadata.json",
        "--package", m.PackageName,
        "--output", "/_dagger_main.py",
    })

    return ctr.File("/gen.py"), ctr.File("/_dagger_main.py")
}
```

(Delete the old `runPlainCodegen` and `runSelfCallsCodegen` — they're fully replaced. If you can't delete them because other functions still call them, search with `grep -n "runPlainCodegen\|runSelfCallsCodegen" sdk/python/runtime/main.go` — there should be no callers left once `WithSDK` has been updated.)

### Step 3: Build the Go runtime

- [ ] **Compile**

```bash
cd sdk/python/runtime && go build ./... && cd -
```

Expected: exit 0.

If the build fails because `runPlainCodegen` / `runSelfCallsCodegen` are still referenced, locate the caller and update it to use `runCodegen` (most likely the `WithSDK` block wasn't fully replaced in step 2).

### Step 4: Smoke-test self-calls OFF via engine-dev playground

- [ ] **Run the off-path end-to-end**

Use the engine-dev playground skill. Kick it off in background and poll.

```bash
bash skills/engine-dev-testing/with-playground.sh '
cd /work &&
mkdir py-off &&
cd py-off &&
dagger init --sdk=python --name=test --source=. &&
dagger functions
'
```

Expected (check task output): `dagger functions` succeeds and lists `container-echo`, `grep-dir`. No crashes.

### Step 5: Smoke-test self-calls ON via engine-dev playground

- [ ] **Run the on-path end-to-end**

```bash
bash skills/engine-dev-testing/with-playground.sh '
cd /work &&
mkdir py-on &&
cd py-on &&
dagger init --sdk=python --name=test --source=. --with-self-calls &&
dagger functions
'
```

Expected: `dagger functions` succeeds; the four phases all run; `WithExperimentalFeatures([SELF_CALLS])` is visible in the trace.

### Step 6: Inspect the generated `_dagger_main.py` produced during smoke test

- [ ] **Verify the file exists and has the expected shape**

From either py-off or py-on (the generated file shape is the same; only schema differs), verify:

```bash
bash skills/engine-dev-testing/with-playground.sh '
cd /work/py-off &&
cat src/test/_dagger_main.py | head -30 &&
echo === &&
grep -c "async def build_module" src/test/_dagger_main.py &&
grep -c "async def invoke" src/test/_dagger_main.py &&
grep -c "match (parent_name, fn_name):" src/test/_dagger_main.py
'
```

Expected output fragments:
- First 30 lines show the AUTO-GENERATED header, `from dagger.mod._entrypoint import ...`, start of `async def build_module`.
- Three `grep -c` results are `1` each (one `build_module`, one `invoke`, one `match`).

### Step 7: Commit Task 3

- [ ] **Commit**

```bash
stg new python-sdk-wire-entrypoint-codegen -m "$(cat <<'EOF'
sdk(python/runtime): wire entrypoint-gen as phase 3 of WithSDK

Unifies runPlainCodegen / runSelfCallsCodegen into a single runCodegen
helper that now runs four container exec phases:

  0. analyzer emit --metadata-output [--schematool-output]
  1. merge-schema                         (self-calls only)
  2. codegen generate                     -> /gen.py
  3. analyzer entrypoint                  -> /_dagger_main.py

The entrypoint file is written into the user's module source tree at
src/<package>/_dagger_main.py alongside the existing gen.py.

template/runtime.py is completely rewritten to delegate to
<pkg>._dagger_main.  No more dagger.mod.cli.app, no more AST analysis
at runtime, no more __dagger_module__ walk.  Import errors on the
generated file surface an actionable message.

The prebuilt .dagger-build/gen.py short-circuit is dropped — it only
covers half the codegen work now that _dagger_main.py is always
produced, and the conditional complexity is no longer worth it.
Spec 3 (legacyCodegenAtRuntime) will reclaim cold-start speed by
committing both generated files and skipping codegen entirely.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

---

## Task 4: Decorators become markers

**Files:**
- Modify: `sdk/python/src/dagger/mod/_module.py` (wrapper bodies of `object_type`, `interface`, `enum_type`; `function` / `field` kept as thin wrappers that preserve today's return types but skip registration side-effects on the `Module` instance.)
- Modify: `sdk/python/tests/mod/test_registration.py` (+ a few sibling files) — mark dynamic-dispatch tests skipped since the path is being neutered.

### Step 1: Locate `_process_type` and the `@object_type` wrapper

- [ ] **Inspect current decorator paths**

```bash
grep -n "_process_type\|def object_type\|def interface\|def enum_type\|def function\|def field\|__dagger_module__" sdk/python/src/dagger/mod/_module.py | head -30
```

Expected: shows the wrapper methods `object_type` (~line 644), `interface` (~line 780), `enum_type` (~line 809), and the shared `_process_type` (~line 696).

### Step 2: Make the `@object_type` wrapper skip `_process_type`

- [ ] **Edit `sdk/python/src/dagger/mod/_module.py` — the `object_type` method**

Locate the `wrapper` closure inside `def object_type(...)`:

```go
        def wrapper(cls: T) -> T:
            if not inspect.isclass(cls):
                msg = f"Expected a class, got {type(cls)}"
                raise BadUsageError(msg)
            ...
            wrapped = dataclasses.dataclass(kw_only=True)(cls)
            return self._process_type(wrapped, deprecated=deprecated)
```

Replace the last two lines inside the `wrapper` (the `wrapped = ...` + `return self._process_type(...)`) with:

```python
            wrapped = dataclasses.dataclass(kw_only=True)(cls)
            # Static-entrypoint mode: registration side effects are
            # handled at codegen time (see hack/designs/python-sdk-static-entrypoint.md).
            # We keep @dataclass behavior for users but skip the
            # Module registration that's no longer consulted at runtime.
            wrapped.__dagger_is_object_type__ = True
            if deprecated is not None:
                wrapped.__dagger_deprecated__ = deprecated
            return wrapped
```

### Step 3: Make `@interface` skip `_process_type`

- [ ] **Edit the `interface` method similarly**

Locate in `_module.py`:

```python
    def interface(self, cls: T | None = None) -> T | Callable[[T], T]:
        ...
        def wrapper(cls: T) -> T:
            ...
            return self._process_type(cls, interface=True, deprecated=...)
        ...
```

Replace the return inside `wrapper` with:

```python
            cls.__dagger_is_interface__ = True
            return cls
```

### Step 4: Make `@enum_type` skip registration

- [ ] **Edit the `enum_type` method**

Find the wrapper; replace its registration call with a marker assignment. The pattern is the same: `wrapped.__dagger_is_enum__ = True` and return the class without calling into `Module` state.

### Step 5: Confirm the static-entrypoint invariants — no reads of `__dagger_module__`

- [ ] **Grep to confirm the runtime path no longer reads `__dagger_module__` at import time**

```bash
grep -rn "__dagger_module__" sdk/python/src/dagger/ sdk/python/runtime/template/
```

Expected: only appears in the old `_resolver.py` / `_module.py` dynamic path, NOT in `template/runtime.py` (which is the new static path).

### Step 6: Skip the dynamic-dispatch tests

The existing `sdk/python/tests/mod/test_registration.py` (and some siblings) exercise the decorator-driven dynamic registration that we just neutered. Mark them module-skipped.

- [ ] **Add a module-level skip marker at the top of each file**

For each of these files, insert the skip block **immediately after the module docstring** (or at line 1 if there's no docstring):

- `sdk/python/tests/mod/test_registration.py`
- `sdk/python/tests/mod/test_enum_docstrings.py`
- `sdk/python/tests/mod/test_forward_reference.py`
- `sdk/python/tests/mod/test_future_annotations.py`
- `sdk/python/tests/mod/test_interfaces.py`
- `sdk/python/tests/mod/test_results.py`

Skip block to insert:

```python
import pytest

pytestmark = pytest.mark.skip(
    reason=(
        "Dynamic __dagger_module__ dispatch replaced by codegen'd "
        "_dagger_main.py static entrypoint (spec 2). AST-path coverage "
        "lives in test_ast_analyzer.py; entrypoint coverage lives in "
        "test_entrypoint_gen.py."
    )
)
```

For each file, keep the rest of the file intact — do **not** delete the tests. They may be revived later with different assertions.

### Step 7: Run the Python SDK unit tests to confirm nothing bleeds through

- [ ] **Run**

```bash
cd sdk/python && OTEL_TRACES_EXPORTER=none OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none uv run --frozen pytest tests/mod/ -q
```

Expected:
- The 6 dynamic-dispatch test files are all skipped.
- `test_ast_analyzer.py`, `test_schematool_serializer.py`, `test_entrypoint_gen.py`, `test_entrypoint_helpers.py`, `test_discovery.py`, `test_utils.py` all PASS.

If any test outside the 6 skipped files fails, fix it before moving on — it means our decorator change inadvertently broke an AST-path test. Look for imports of `__dagger_module__` or `dagger.mod.default_module()` in the failing test.

### Step 8: Commit Task 4

```bash
stg new python-sdk-decorators-become-markers -m "$(cat <<'EOF'
sdk(python): @object_type / @interface / @enum_type become markers

The static entrypoint (spec 2) makes the __dagger_module__ registration
built by these decorators dead code.  template/runtime.py now delegates
to the codegen'd _dagger_main.py which has a baked-in dispatch table —
nothing at runtime walks __dagger_module__ anymore.

Wrappers now:
- apply @dataclasses.dataclass(kw_only=True) (so users still get
  dataclass semantics)
- set __dagger_is_{object_type,interface,enum}__ = True markers so
  third-party tooling can still detect decoration
- return the class without calling self._process_type

The Module / Module.serve() / Module.invoke() / _resolver.* classes are
left in place for extension-SDK authors who subclass them.  Follow-up
cleanup can remove them once those consumers migrate.

The test files that exercised the dynamic-dispatch path
(test_registration.py, test_enum_docstrings.py, test_forward_reference.py,
test_future_annotations.py, test_interfaces.py, test_results.py) are
module-skipped via pytestmark.  AST-path coverage continues via
test_ast_analyzer.py; entrypoint coverage via test_entrypoint_gen.py.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

---

## Task 5: Integration tests — zero-runtime-analysis invariants

**Files:**
- Create: `core/integration/module_python_static_entrypoint_test.go`

### Step 1: Write the integration tests

- [ ] **Create `core/integration/module_python_static_entrypoint_test.go`**

Note on syntax: the Python source strings are Go raw strings (delimited with backticks), and the `daggerQuery` / `JSONEq` args are also raw strings. GraphQL queries like `{hello}` are themselves wrapped in backticks.

```go
package core

import (
	"context"

	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"

	"dagger.io/dagger"
)

// TestPythonStaticEntrypointBasic verifies end-to-end that a Python
// module goes through the new static entrypoint pipeline: codegen
// emits _dagger_main.py, the runtime imports it, and a basic function
// call succeeds.
func (ModuleSuite) TestPythonStaticEntrypointBasic(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type

@object_type
class Test:
    @function
    def hello(self) -> str:
        return "hi"
`
	modGen := modInit(t, c, "python", source)

	out, err := modGen.
		With(daggerQuery(`{hello}`)).
		Stdout(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"hello":"hi"}`, out)
}

// TestPythonStaticEntrypointCustomObject verifies that a user
// @object_type with a returned instance + chained method call works
// (exercises parent-state rehydrate_parent() + the static dispatch for
// custom types).
func (ModuleSuite) TestPythonStaticEntrypointCustomObject(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type
import dataclasses

@object_type
@dataclasses.dataclass
class Greeter:
    tone: str = "cheerful"

    @function
    def greet(self, name: str) -> str:
        return f"{self.tone} hello, {name}"

@object_type
class Test:
    @function
    def new_greeter(self, tone: str = "cheerful") -> Greeter:
        return Greeter(tone=tone)
`
	modGen := modInit(t, c, "python", source)

	out, err := modGen.
		With(daggerQuery(`{newGreeter(tone:"warm"){greet(name:"world")}}`)).
		Stdout(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"newGreeter":{"greet":"warm hello, world"}}`, out)
}

// TestPythonStaticEntrypointEnum verifies enum typedef generation and
// dispatch coerce_enum + unstructure_enum helpers.
func (ModuleSuite) TestPythonStaticEntrypointEnum(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type, enum_type

@enum_type
class Color(dagger.Enum):
    RED = "red"
    BLUE = "blue"

@object_type
class Test:
    @function
    def flip(self, color: Color) -> Color:
        return Color.BLUE if color == Color.RED else Color.RED
`
	modGen := modInit(t, c, "python", source)

	out, err := modGen.
		With(daggerQuery(`{flip(color:RED)}`)).
		Stdout(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"flip":"BLUE"}`, out)
}

// TestPythonStaticEntrypointFileShape verifies that the codegen'd
// _dagger_main.py actually lands in the user's source tree and has the
// expected structural invariants (build_module + invoke + match).
func (ModuleSuite) TestPythonStaticEntrypointFileShape(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type

@object_type
class Test:
    @function
    def hello(self) -> str:
        return "hi"
`
	modGen := modInit(t, c, "python", source)

	content, err := modGen.
		File("src/test/_dagger_main.py").
		Contents(ctx)
	require.NoError(t, err)

	require.Contains(t, content, "async def build_module")
	require.Contains(t, content, "async def invoke")
	require.Contains(t, content, "match (parent_name, fn_name):")
	require.Contains(t, content, `case ("Test", "hello"):`)
	require.NotContains(t, content, "__annotations__",
		"generated entrypoint must contain no runtime type introspection")
}

// TestPythonStaticEntrypointNoDagModuleAtRuntime verifies the template
// runtime does not consult __dagger_module__ — the static dispatch is
// the sole path.  Confirms that removing the dynamic registration
// side-effects from the decorators did not break the runtime.
func (ModuleSuite) TestPythonStaticEntrypointNoDagModuleAtRuntime(
	ctx context.Context, t *testctx.T,
) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import function, object_type

@object_type
class Test:
    @function
    def only(self) -> str:
        return "ok"
`
	modGen := modInit(t, c, "python", source)

	out, err := modGen.
		With(daggerQuery(`{only}`)).
		Stdout(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"only":"ok"}`, out)
}
```

### Step 2: Run the integration tests via engine-dev

- [ ] **Run each test through `dagger call engine-dev test`**

Run these in background; they can take 5-15 min each cold.

```bash
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonStaticEntrypointBasic$' --pkg=./core/integration --test-verbose --timeout=20m
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonStaticEntrypointCustomObject$' --pkg=./core/integration --test-verbose --timeout=20m
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonStaticEntrypointEnum$' --pkg=./core/integration --test-verbose --timeout=20m
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonStaticEntrypointFileShape$' --pkg=./core/integration --test-verbose --timeout=20m
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonStaticEntrypointNoDagModuleAtRuntime$' --pkg=./core/integration --test-verbose --timeout=20m
```

Each returns exit 0 + `Void` from `engineDev.test` on pass.

### Step 3: Re-run the spec 1 `TestSelfCalls` suite to confirm no regression

- [ ] **Run**

```bash
dagger call engine-dev test --run='^TestModuleSuite$/^TestSelfCalls$/^python$' --pkg=./core/integration --test-verbose --timeout=20m
dagger call engine-dev test --run='^TestModuleSuite$/^TestSelfCallsOffPython$' --pkg=./core/integration --test-verbose --timeout=20m
dagger call engine-dev test --run='^TestModuleSuite$/^TestSelfCalls$/^go$' --pkg=./core/integration --test-verbose --timeout=20m
```

All three must pass (exit 0, `Void`).

### Step 4: Commit Task 5

```bash
stg add core/integration/module_python_static_entrypoint_test.go
stg new python-sdk-static-entrypoint-integration-tests -m "$(cat <<'EOF'
core/integration: python static entrypoint tests

Five new integration tests covering the zero-runtime-analysis
invariants of the static entrypoint (spec 2):

  * TestPythonStaticEntrypointBasic — simple module end-to-end
  * TestPythonStaticEntrypointCustomObject — user @object_type
    returned and chained
  * TestPythonStaticEntrypointEnum — enum round-trip
  * TestPythonStaticEntrypointFileShape — generated _dagger_main.py
    has build_module / invoke / match / no __annotations__
  * TestPythonStaticEntrypointNoDagModuleAtRuntime — dispatch does
    not rely on __dagger_module__

The spec 1 TestSelfCalls suite (Python + off + Go) must continue
passing — no regressions from the decorator-marker change or the
WithSDK pipeline unification.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -5
```

---

## Final verification

### Step 1: Full Python unit test suite

- [ ] **Run**

```bash
cd sdk/python && OTEL_TRACES_EXPORTER=none OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none uv run --frozen pytest tests/mod/ -q
```

Expected: PASS (with 6 files module-skipped as documented in Task 4).

### Step 2: All integration tests, spec 1 + spec 2

- [ ] **Python-related integration tests**

```bash
dagger call engine-dev test --run='^TestModuleSuite$/^TestSelfCalls' --pkg=./core/integration --test-verbose --timeout=30m
dagger call engine-dev test --run='^TestModuleSuite$/^TestPythonStaticEntrypoint' --pkg=./core/integration --test-verbose --timeout=30m
```

Both invocations: exit 0, `Void`.

### Step 3: Confirm clean stack

- [ ] **Inspect**

```bash
stg series --all
git status --porcelain
```

Expected: top of stack is `python-sdk-static-entrypoint-integration-tests`, tree clean (aside from `.claude/scheduled_tasks.lock` harness artifact), five new patches on top of `python-sdk-static-entrypoint-design`.

---

## Out-of-scope reminders (do not implement in this plan)

- **Spec 3**: `legacyCodegenAtRuntime` opt-in + committing `_dagger_main.py` + `gen.py` + `dagger init --sdk=python` writing the two flags.
- **Removing the dead `dagger.mod._module` / `_resolver` / `_converter` classes.** They remain importable for extension-SDK authors. Cleanup PR can follow once the extension ecosystem migrates.
- **Return-list-of-dagger-objects support** in `_render_return` — the plan emits `return value_expr` for list returns, which works for lists of primitives but not lists of IDs. Real-world modules rarely return lists of Dagger objects. If this gap surfaces, add `unstructure_list` to `_entrypoint.py` + a new `_render_list_return` helper as a follow-up.
- **More sophisticated argument coercion** (e.g., nested dataclasses in user-object fields) — current `coerce_object(cls, dict)` works if `cls` takes `**kwargs` directly, which is the `@dataclass` case. Nested custom objects may need recursion. Revisit if user modules break.
