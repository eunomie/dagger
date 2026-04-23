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


def test_coerce_enum_constructs_enum_from_name():
    """Name-based lookup — works even when value differs from name."""
    assert coerce_enum(_Color, "RED") is _Color.RED
    assert coerce_enum(_Color, "BLUE") is _Color.BLUE


def test_coerce_list_applies_element_coercer():
    out = coerce_list(int, ["1", "2", "3"])
    assert out == [1, 2, 3]


def test_coerce_list_with_identity_passes_through():
    out = coerce_list(lambda x: x, ["a", "b"])
    assert out == ["a", "b"]


@pytest.mark.anyio
async def test_unstructure_id_awaits_id_method():
    class _Obj:
        async def id(self):
            return "ID-xyz"

    assert await unstructure_id(_Obj()) == "ID-xyz"


def test_unstructure_dataclass_returns_dict():
    assert unstructure_dataclass(_Foo(x=3, y="k")) == {"x": 3, "y": "k"}


def test_unstructure_enum_returns_name():
    """Returns the member name, not the value — engine identifies by name."""
    assert unstructure_enum(_Color.RED) == "RED"


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
    "import inspect",
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
