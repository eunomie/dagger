"""Tests for the _analyzer → schematool.ModuleTypes serializer."""

from __future__ import annotations

from dagger.mod._analyzer.analyze import analyze_source_string
from dagger.mod._analyzer.schematool import to_schematool_json


# Base-schema type names that show up in the serializer tests below.
# These represent types that already exist in the engine's introspection
# schema (e.g. Container, Directory) — the serializer must filter them
# out so schematool.Merge does not see collisions.
_BASE_TYPE_NAMES: frozenset[str] = frozenset({"Container", "Directory", "File"})


def test_serializes_object_with_function_and_field():
    md = analyze_source_string(
        """
import dagger

@dagger.object_type
class Test:
    greeting: str = dagger.field(default="hi")

    @dagger.function
    def echo(self, msg: str) -> str: ...
""",
        "Test",
        module_name="test",
    )

    out = to_schematool_json(md, _BASE_TYPE_NAMES)

    assert out["name"] == "test"
    assert len(out["objects"]) == 1
    obj = out["objects"][0]
    assert obj["name"] == "Test"
    field_names = [f["name"] for f in obj["fields"]]
    assert field_names == ["greeting"]
    fn_names = [f["name"] for f in obj["functions"]]
    assert fn_names == ["echo"]
    echo = obj["functions"][0]
    assert echo["returnType"]["kind"] == "NON_NULL"
    assert echo["returnType"]["ofType"]["kind"] == "SCALAR"
    assert echo["returnType"]["ofType"]["name"] == "String"
    assert len(echo["args"]) == 1
    assert echo["args"][0]["name"] == "msg"


def test_serializes_enum():
    md = analyze_source_string(
        """
import dagger

@dagger.enum_type
class Color(dagger.Enum):
    RED = "red"
    BLUE = "blue"

@dagger.object_type
class Test:
    @dagger.function
    def pick(self) -> Color: ...
""",
        "Test",
        module_name="test",
    )

    out = to_schematool_json(md, _BASE_TYPE_NAMES)

    assert len(out["enums"]) == 1
    enum = out["enums"][0]
    assert enum["name"] == "Color"
    values = [v["name"] for v in enum["values"]]
    assert values == ["RED", "BLUE"]


def test_list_return_type_nested_non_null():
    md = analyze_source_string(
        """
import dagger

@dagger.object_type
class Test:
    @dagger.function
    def many(self) -> list[str]: ...
""",
        "Test",
        module_name="test",
    )

    out = to_schematool_json(md, _BASE_TYPE_NAMES)
    ret = out["objects"][0]["functions"][0]["returnType"]
    # NON_NULL(LIST(NON_NULL(String)))
    assert ret["kind"] == "NON_NULL"
    assert ret["ofType"]["kind"] == "LIST"
    assert ret["ofType"]["ofType"]["kind"] == "NON_NULL"
    assert ret["ofType"]["ofType"]["ofType"]["name"] == "String"


def test_optional_param_with_default():
    md = analyze_source_string(
        """
import dagger

@dagger.object_type
class Test:
    @dagger.function
    def echo(self, msg: str = "hi") -> str: ...
""",
        "Test",
        module_name="test",
    )

    arg = to_schematool_json(md, _BASE_TYPE_NAMES)["objects"][0]["functions"][0]["args"][0]
    assert arg["name"] == "msg"
    assert arg["defaultValue"] == '"hi"'
    # optional param → the type is nullable (no outer NON_NULL)
    assert arg["type"]["kind"] == "SCALAR"
    assert arg["type"]["name"] == "String"


def test_private_attribute_excluded():
    md = analyze_source_string(
        """
import dagger

@dagger.object_type
class Test:
    private_only: str                    # no dagger.field() → excluded
    visible_field: str = dagger.field()
""",
        "Test",
        module_name="test",
    )

    fields = to_schematool_json(md, _BASE_TYPE_NAMES)["objects"][0]["fields"]
    assert [f["name"] for f in fields] == ["visible_field"]


def test_references_to_base_schema_types_not_re_declared():
    md = analyze_source_string(
        """
import dagger

@dagger.object_type
class Test:
    @dagger.function
    def make(self) -> dagger.Container: ...
""",
        "Test",
        module_name="test",
    )

    out = to_schematool_json(md, _BASE_TYPE_NAMES)
    # Only "Test" is user-declared; Container is filtered.
    declared = {o["name"] for o in out["objects"]}
    assert declared == {"Test"}
    # The function's return type still references Container.
    ret = out["objects"][0]["functions"][0]["returnType"]
    assert ret["kind"] == "NON_NULL"
    assert ret["ofType"]["name"] == "Container"


def test_serializes_interface():
    md = analyze_source_string(
        """
import dagger

@dagger.interface
class Quacker:
    \"\"\"Something that quacks.\"\"\"

    @dagger.function
    def quack(self) -> str: ...

@dagger.object_type
class Test:
    @dagger.function
    def make(self) -> Quacker: ...
""",
        "Test",
        module_name="test",
    )

    out = to_schematool_json(md, _BASE_TYPE_NAMES)

    # Interface must appear in "interfaces", not "objects"
    object_names = {o["name"] for o in out["objects"]}
    assert "Quacker" not in object_names

    assert "interfaces" in out
    assert len(out["interfaces"]) == 1
    iface = out["interfaces"][0]
    assert iface["name"] == "Quacker"
    assert iface["description"] == "Something that quacks."
    # Only name, description, and functions — no fields, no constructor
    assert "fields" not in iface
    assert "constructor" not in iface
    fn_names = [f["name"] for f in iface["functions"]]
    assert fn_names == ["quack"]


# -- CLI tests ---------------------------------------------------------------


def test_cli_emits_schematool_json(tmp_path):
    """End-to-end: python -m dagger.mod._analyzer emit writes valid JSON."""
    import json
    import subprocess
    import sys

    src = tmp_path / "main.py"
    src.write_text(
        """
import dagger

@dagger.object_type
class Test:
    @dagger.function
    def echo(self, msg: str) -> str: ...
""".lstrip()
    )

    schema = tmp_path / "schema.json"
    schema.write_text(
        json.dumps(
            {
                "__schema": {
                    "types": [
                        {"name": "Container", "kind": "OBJECT"},
                        {"name": "Directory", "kind": "OBJECT"},
                        {"name": "File", "kind": "OBJECT"},
                    ]
                }
            }
        )
    )

    out = tmp_path / "module-types.json"

    result = subprocess.run(
        [
            sys.executable,
            "-m",
            "dagger.mod._analyzer",
            "emit",
            "--module-source-dir",
            str(tmp_path),
            "--main-object",
            "Test",
            "--module-name",
            "test",
            "--introspection-json",
            str(schema),
            "--output",
            str(out),
        ],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode == 0, result.stderr
    payload = json.loads(out.read_text())
    assert payload["name"] == "test"
    assert [o["name"] for o in payload["objects"]] == ["Test"]
