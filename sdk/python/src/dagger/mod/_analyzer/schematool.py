"""Convert ModuleMetadata into schematool.ModuleTypes JSON.

This is the wire format consumed by ``cmd/codegen merge-schema``; see
``cmd/codegen/schematool/module_types.go`` for the canonical shape.
"""

from __future__ import annotations

from typing import Any

from dagger.mod._analyzer.metadata import (
    EnumTypeMetadata,
    FieldMetadata,
    FunctionMetadata,
    ModuleMetadata,
    ObjectTypeMetadata,
    ParameterMetadata,
    ResolvedType,
)
from dagger.mod._analyzer.parser import normalize_name, to_api_name

# ResolvedType.kind -> schematool introspection kind (for named types).
# LIST and NON_NULL wrappers are applied at the TypeRef level below.
_KIND_MAP: dict[str, str] = {
    "object": "OBJECT",
    "interface": "INTERFACE",
    "enum": "ENUM",
    "scalar": "SCALAR",
    "primitive": "SCALAR",
}

# Python primitive names -> canonical GraphQL scalar names in the
# engine's base schema.
_PRIMITIVE_TYPE_NAMES: dict[str, str] = {
    "str": "String",
    "int": "Int",
    "float": "Float",
    "bool": "Boolean",
}

# Dagger object types -> their ID scalar names.  When a Dagger built-in
# type is used as a *function argument* the engine passes it as an ID
# scalar (e.g. Directory → DirectoryID).  GraphQL requires argument
# types to be input types (SCALAR, INPUT_OBJECT, or ENUM), not OBJECT
# types, so we must emit the ID scalar rather than the object itself.
_DAGGER_OBJECT_ID_SCALARS: dict[str, str] = {
    "Container": "ContainerID",
    "Directory": "DirectoryID",
    "File": "FileID",
    "Secret": "SecretID",
    "Service": "ServiceID",
    "CacheVolume": "CacheVolumeID",
    "Socket": "SocketID",
    "ModuleSource": "ModuleSourceID",
    "Module": "ModuleID",
    "GitRepository": "GitRepositoryID",
    "GitRef": "GitRefID",
    "Terminal": "TerminalID",
}


def to_schematool_json(
    metadata: ModuleMetadata,
    base_type_names: frozenset[str],
) -> dict[str, Any]:
    """Serialize ``metadata`` into a schematool.ModuleTypes dict.

    ``base_type_names`` is the set of type names already present in the
    engine's introspection schema. Objects / enums declared in the
    module that collide with a base type are dropped from the output so
    ``schematool.Merge`` does not reject the payload on duplicate names.
    """
    objects = [
        _object_to_dict(obj)
        for obj in metadata.objects.values()
        if not obj.is_interface and obj.name not in base_type_names
    ]
    interfaces = [
        _interface_to_dict(obj)
        for obj in metadata.objects.values()
        if obj.is_interface and obj.name not in base_type_names
    ]
    enums = [
        _enum_to_dict(enum)
        for enum in metadata.enums.values()
        if enum.name not in base_type_names
    ]

    out: dict[str, Any] = {
        "name": metadata.module_name,
        "objects": objects,
        "enums": enums,
    }
    if interfaces:
        out["interfaces"] = interfaces
    if metadata.doc:
        out["description"] = metadata.doc
    return out


def _object_to_dict(obj: ObjectTypeMetadata) -> dict[str, Any]:
    out: dict[str, Any] = {"name": obj.name}
    if obj.doc:
        out["description"] = obj.doc
    if obj.fields:
        out["fields"] = [_field_to_dict(f) for f in obj.fields]
    if obj.functions:
        out["functions"] = [_function_to_dict(f) for f in obj.functions]
    if obj.constructor is not None:
        out["constructor"] = _function_to_dict(obj.constructor)
    return out


def _interface_to_dict(obj: ObjectTypeMetadata) -> dict[str, Any]:
    out: dict[str, Any] = {"name": obj.name}
    if obj.doc:
        out["description"] = obj.doc
    if obj.functions:
        out["functions"] = [_function_to_dict(f) for f in obj.functions]
    return out


def _enum_to_dict(enum: EnumTypeMetadata) -> dict[str, Any]:
    values = []
    for m in enum.members:
        v: dict[str, Any] = {"name": m.name}
        if m.doc:
            v["description"] = m.doc
        if m.value and m.value != m.name:
            v["value"] = m.value
        values.append(v)
    out: dict[str, Any] = {"name": enum.name, "values": values}
    if enum.doc:
        out["description"] = enum.doc
    return out


def _field_to_dict(field: FieldMetadata) -> dict[str, Any]:
    # api_name from the analyzer uses normalize_name (snake_case).
    # The schematool JSON must carry camelCase names so that the
    # generated gen.py uses the same names as the live engine schema.
    graphql_name = (
        field.api_name
        if _is_explicit_name(field.api_name, field.python_name)
        else to_api_name(field.api_name)
    )
    out: dict[str, Any] = {
        "name": graphql_name,
        "type": _resolved_to_typeref(field.resolved_type),
    }
    if field.doc:
        out["description"] = field.doc
    return out


def _function_to_dict(fn: FunctionMetadata) -> dict[str, Any]:
    # api_name from the analyzer uses normalize_name (snake_case).
    # Convert to camelCase for the schematool JSON so the generated
    # gen.py matches the live engine schema's camelCase field names.
    graphql_name = (
        fn.api_name
        if _is_explicit_name(fn.api_name, fn.python_name)
        else to_api_name(fn.api_name)
    )
    out: dict[str, Any] = {
        "name": graphql_name,
        "returnType": _resolved_to_typeref(fn.resolved_return_type),
    }
    if fn.doc:
        out["description"] = fn.doc
    if fn.parameters:
        out["args"] = [_param_to_dict(p) for p in fn.parameters]
    return out


def _param_to_dict(param: ParameterMetadata) -> dict[str, Any]:
    # Optional parameters (default value OR nullable) => drop the outer
    # NON_NULL wrapper at the TypeRef level.
    #
    # If the parameter is a Dagger built-in object type (e.g. Directory,
    # Container), substitute the corresponding ID scalar.  GraphQL requires
    # argument types to be INPUT types (SCALAR, ENUM, or INPUT_OBJECT), not
    # OBJECT types.  The engine passes built-in objects as ID scalars at the
    # wire level, so emitting DirectoryID instead of Directory here keeps the
    # merged schema valid and matches what the runtime expects.
    rt = param.resolved_type
    if rt.kind == "object" and rt.name in _DAGGER_OBJECT_ID_SCALARS:
        rt = ResolvedType(
            kind="scalar",
            name=_DAGGER_OBJECT_ID_SCALARS[rt.name],
            is_optional=rt.is_optional,
        )
    type_ref = _resolved_to_typeref(rt, nullable=param.is_optional)
    # api_name from the analyzer uses normalize_name (snake_case).
    # Convert to camelCase so the generated gen.py Arg() names match
    # the live engine schema (which normalises to lowerCamelCase).
    graphql_name = (
        param.api_name
        if _is_explicit_name(param.api_name, param.python_name)
        else to_api_name(param.api_name)
    )
    out: dict[str, Any] = {
        "name": graphql_name,
        "type": type_ref,
    }
    if param.doc:
        out["description"] = param.doc
    if param.has_default:
        out["defaultValue"] = _render_default(param.default_value)
    return out


def _is_explicit_name(api_name: str, python_name: str) -> bool:
    """Return True when api_name was explicitly supplied by the user (e.g.,
    via ``@dagger.function(name="myName")``).  In that case we honour it
    verbatim rather than applying an automatic camelCase conversion.

    An explicit name differs from what normalize_name(python_name) would
    produce, which is the value assigned automatically.
    """
    return api_name != normalize_name(python_name)


def _resolved_to_typeref(
    rt: ResolvedType,
    *,
    nullable: bool | None = None,
) -> dict[str, Any]:
    """Convert a ResolvedType to an introspection TypeRef.

    ``nullable`` overrides ``rt.is_optional`` at the outermost level
    (used for parameters where optionality comes from default/nullable
    handling rather than ``T | None`` in the annotation).
    """
    is_nullable = rt.is_optional if nullable is None else nullable

    if rt.kind == "void":
        inner: dict[str, Any] = {"kind": "SCALAR", "name": "Void"}
    elif rt.kind == "list":
        assert rt.element_type is not None, "list kind must carry element_type"
        inner = {
            "kind": "LIST",
            "ofType": _resolved_to_typeref(rt.element_type),
        }
    else:
        name = rt.name
        if rt.kind == "primitive":
            name = _PRIMITIVE_TYPE_NAMES.get(rt.name, rt.name)
        inner = {"kind": _KIND_MAP[rt.kind], "name": name}

    if is_nullable:
        return inner
    return {"kind": "NON_NULL", "ofType": inner}


def _render_default(value: Any) -> str:
    """Render a default value as a GraphQL-style string (best effort).

    schematool accepts this as a free-form string; the engine parses
    it when coercing argument values.
    """
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        return str(value)
    if isinstance(value, str):
        # quote and escape backslashes + double quotes
        escaped = value.replace("\\", "\\\\").replace('"', '\\"')
        return f'"{escaped}"'
    # Lists / dicts / other — fall back to repr (good enough for tests;
    # real code rarely ships non-scalar defaults through codegen).
    return repr(value)
