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

# Python primitive -> dagger.TypeDefKind enum value.
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
    for obj in md.objects.values():
        if obj.is_interface:
            lines.extend(_indent(_render_with_interface(obj), "        "))
        else:
            lines.extend(_indent(_render_with_object(obj), "        "))
    for enum in md.enums.values():
        lines.extend(_indent(_render_with_enum(enum), "        "))
    lines.append("    )")
    return "\n".join(lines)


def _should_emit_constructor(fn: FunctionMetadata | None) -> bool:
    """Match registration.py's guard: only emit a constructor when it
    carries meaningful metadata (classmethod / __init__ / has params /
    has docstring). Trivial auto-generated constructors are skipped to
    keep the generated typedefs consistent with the dynamic path.
    """
    if fn is None:
        return False
    if fn.is_classmethod:
        return True
    if fn.python_name == "__init__":
        return True
    if fn.parameters:
        return True
    if fn.doc:
        return True
    return False


def _render_with_object(obj: ObjectTypeMetadata) -> list[str]:
    typedef_expr = (
        f'    _dag.type_def().with_object({json.dumps(obj.name)})'
        + _optional_description_arg(obj.doc)
    )
    if obj.deprecated:
        typedef_expr += f".with_deprecated({json.dumps(obj.deprecated)})"
    lines = [
        f'.with_object(',
        typedef_expr,
    ]
    for fn in obj.functions:
        lines.extend(_indent(_render_function_chain(fn), "    "))
    for fd in obj.fields:
        lines.extend(_indent(_render_field_chain(fd), "    "))
    if _should_emit_constructor(obj.constructor):
        lines.extend(_indent(_render_constructor_chain(obj.constructor), "    "))  # type: ignore[arg-type]
    lines.append(")")
    return lines


def _render_with_interface(obj: ObjectTypeMetadata) -> list[str]:
    lines = [
        f'.with_interface(',
        f'    _dag.type_def().with_interface({json.dumps(obj.name)})'
        + _optional_description_arg(obj.doc),
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
    if fn.deprecated:
        lines.append(f"    .with_deprecated({json.dumps(fn.deprecated)})")
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
    parts = [json.dumps(to_api_name(fd.api_name)), td]
    if fd.doc:
        parts.append(f"description={json.dumps(fd.doc)}")
    if fd.deprecated:
        parts.append(f"deprecated={json.dumps(fd.deprecated)}")
    return [f".with_field({', '.join(parts)})"]


def _render_arg_chain(param: ParameterMetadata) -> list[str]:
    api = to_api_name(param.api_name)
    td = _render_typedef(param.resolved_type, nullable=param.is_optional)
    args = [json.dumps(api), td]
    if param.has_default:
        args.append(f"default_value=dagger.JSON({json.dumps(json.dumps(param.default_value))})")
    if param.doc:
        args.append(f"description={json.dumps(param.doc)}")
    if param.default_path:
        args.append(f"default_path={json.dumps(param.default_path)}")
    if param.default_address:
        args.append(f"default_address={json.dumps(param.default_address)}")
    if param.deprecated:
        args.append(f"deprecated={json.dumps(param.deprecated)}")
    if param.ignore:
        args.append(f"ignore={json.dumps(param.ignore)}")
    return [f".with_arg({', '.join(args)})"]


def _render_typedef(rt: ResolvedType, *, nullable: bool | None = None) -> str:
    """Render a ResolvedType as a ``_dag.type_def()...`` expression string."""
    is_nullable = rt.is_optional if nullable is None else nullable
    expr = _render_typedef_inner(rt)
    if not is_nullable:
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
            continue
        for fn in obj.functions:
            lines.extend(_render_function_case(obj, fn, package))
        for fd in obj.fields:
            lines.extend(_render_field_case(obj, fd, package))
        if _should_emit_constructor(obj.constructor):
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

    nullable = param.is_nullable

    if rt.kind == "object" and rt.name in _DAGGER_OBJECT_LOADERS:
        loader = _DAGGER_OBJECT_LOADERS[rt.name]
        if nullable:
            return [
                f'            _raw = {input_expr}',
                f'            {var} = coerce_id({loader}, _raw) if _raw is not None else None',
            ]
        return [f'            {var} = coerce_id({loader}, {input_expr})']
    if rt.kind == "object":
        if nullable:
            return [
                f'            from {package} import {rt.name}',
                f'            _raw = {input_expr}',
                f'            {var} = coerce_object({rt.name}, _raw) if _raw is not None else None',
            ]
        return [
            f'            from {package} import {rt.name}',
            f'            {var} = coerce_object({rt.name}, {input_expr})',
        ]
    if rt.kind == "enum":
        if nullable:
            return [
                f'            from {package} import {rt.name}',
                f'            _raw = {input_expr}',
                f'            {var} = coerce_enum({rt.name}, _raw) if _raw is not None else None',
            ]
        return [
            f'            from {package} import {rt.name}',
            f'            {var} = coerce_enum({rt.name}, {input_expr})',
        ]
    if rt.kind == "list":
        assert rt.element_type is not None
        imports = _collect_user_type_imports(rt.element_type, package)
        elem_coercer = _element_coercer_expr(rt.element_type, package)
        if nullable:
            return [
                *imports,
                f'            _raw = {input_expr}',
                f'            {var} = coerce_list({elem_coercer}, _raw) if _raw is not None else None',
            ]
        return [
            *imports,
            f'            {var} = coerce_list({elem_coercer}, {input_expr})',
        ]
    # Primitives: None passes through fine (JSON null); no guard needed.
    return [f'            {var} = {input_expr}']


def _collect_user_type_imports(rt: ResolvedType, package: str) -> list[str]:
    """Collect the `from <package> import <Name>` lines needed to reference
    every user-declared object/enum inside rt (including nested lists).

    Dagger built-in objects (in _DAGGER_OBJECT_LOADERS) are skipped — they
    don't need imports from the user package.
    """
    names: list[str] = []

    def walk(t: ResolvedType) -> None:
        if t.kind == "list" and t.element_type is not None:
            walk(t.element_type)
            return
        if t.kind == "enum":
            names.append(t.name)
            return
        if t.kind == "object" and t.name not in _DAGGER_OBJECT_LOADERS:
            names.append(t.name)
            return

    walk(rt)
    # Deduplicate while preserving order.
    seen: set[str] = set()
    unique: list[str] = []
    for n in names:
        if n not in seen:
            seen.add(n)
            unique.append(n)
    return [f'            from {package} import {n}' for n in unique]


def _element_coercer_expr(rt: ResolvedType, package: str) -> str:
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
        return [f'            return {value_expr}']
    return [f'            return {value_expr}']


def _local_var_for_param(param: ParameterMetadata) -> str:
    return param.python_name


def _render_default(param: ParameterMetadata) -> str:
    v = param.default_value
    if v is None:
        return "None"
    if isinstance(v, bool):
        return "True" if v else "False"
    if isinstance(v, (int, float)):
        return repr(v)
    if isinstance(v, str):
        return json.dumps(v)
    return repr(v)


def _indent(lines: Iterable[str], prefix: str) -> list[str]:
    return [prefix + line if line else line for line in lines]
