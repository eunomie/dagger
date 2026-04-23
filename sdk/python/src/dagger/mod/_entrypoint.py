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
    """Construct an @enum_type instance from its member name.

    The engine sends the member *name* (e.g. ``"ALPHA"``), not the
    associated value (which may be lowercase: ``ALPHA = "alpha"``).
    Name-based lookup (``cls[name]``) correctly round-trips standard
    ``enum.Enum`` classes where name and value differ.
    """
    return cls[value]


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
    """Serialize an enum member to its name.

    Mirrors `coerce_enum`: the engine identifies enum members by name.
    Returning ``obj.name`` works for both ``dagger.Enum`` (deprecated,
    where value == name) and standard ``enum.Enum`` (where name and
    value may differ).
    """
    return obj.name
