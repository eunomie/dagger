# Python SDK: schematool codegen (spec 1) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `dagger init --with-self-calls` work for Python modules by running a three-phase analyze → merge → generate pipeline at codegen time, gated on the `SELF_CALLS` experimental feature.

**Architecture:** A new `moduleSource.experimentalFeatureEnabled` dagql getter lets the Python SDK runtime (itself a Dagger module) query self-calls state. When on, `sdk/python/runtime/main.go::WithSDK` runs a new `dagger.mod._analyzer emit` CLI to produce `schematool.ModuleTypes` JSON, then the existing `cmd/codegen merge-schema` binary (bundled into the Python SDK rootfs as `dist/merge-schema`), then the existing Python `codegen generate` on the extended schema. When off, codegen runs plainly against the base schema (today's behavior).

**Tech Stack:** Go (engine schema + Python SDK Go-side runtime driver + engine-dev build pipeline), Python (analyzer CLI + serializer + unit tests), Dagger integration tests.

**Spec reference:** `hack/designs/python-sdk-schematool-codegen.md`

**StGit workflow:** five new patches stack on top of the current 19. Commit each task with `stg` per repo conventions (not `git commit`); every commit message ends with `Signed-off-by: Yves Brissaud <yves@dagger.io>`; no `Co-Authored-By` lines.

---

## File structure (before tasks)

**New files:**
- `sdk/python/src/dagger/mod/_analyzer/schematool.py` — pure-mapping `to_schematool_json()` function. Takes `ModuleMetadata` + base-schema type-name set; returns a dict matching `cmd/codegen/schematool.ModuleTypes` wire format.
- `sdk/python/src/dagger/mod/_analyzer/__main__.py` — argparse CLI; `emit` subcommand reads source files, runs `analyze_module()`, serializes via `to_schematool_json()`, writes JSON to `--output` or stdout.
- `sdk/python/tests/mod/test_schematool_serializer.py` — unit tests for the serializer (object/enum/list/optional/private/referenced-type cases).

**Modified files:**
- `core/schema/modulesource.go` — register the new dagql field `experimentalFeatureEnabled` (around line 194 where the `withExperimentalFeatures` setter is) + add the handler near the existing `moduleSourceWithExperimentalFeatures` at line 2065.
- `toolchains/engine-dev/build/sdk.go::pythonSDKContent` — add `WithFile("dist/merge-schema", build.CodegenBinary())` inside the rootfs chain around line 98.
- `sdk/python/runtime/main.go` — add `SelfCallsEnabled bool` field on `PythonSdk`, populate in `Load`, branch in `WithSDK`.
- `core/integration/module_test.go::TestSelfCalls` — uncomment Python case (lines 6446-6464); add a new `TestSelfCallsOff` test case verifying gen.py does not contain the module's self types when `--with-self-calls` is absent.

---

## Task 1: dagql getter `moduleSource.experimentalFeatureEnabled`

**Files:**
- Modify: `core/schema/modulesource.go:194-204` (field registration) and `core/schema/modulesource.go:2065` (handler block, just above `moduleSourceWithExperimentalFeatures`)

### Step 1: Add the dagql field registration

- [ ] **Edit `core/schema/modulesource.go` to register the new field**

Insert after the existing `withoutExperimentalFeatures` block (current line 200-204). Locate:

```go
		dagql.Func("withoutExperimentalFeatures", s.moduleSourceWithoutExperimentalFeatures).
			Doc(`Disable experimental features for the module source.`).
			Args(
				dagql.Arg("features").Doc(`The experimental features to disable.`),
			),
```

Insert immediately after that block:

```go
		dagql.Func("experimentalFeatureEnabled", s.moduleSourceExperimentalFeatureEnabled).
			Doc(`Whether the given experimental feature is enabled on this module source.`).
			Args(
				dagql.Arg("feature").Doc(`The experimental feature to check.`),
			),
```

### Step 2: Add the handler

- [ ] **Add the handler function near line 2065**

Insert **above** the existing `func (s *moduleSourceSchema) moduleSourceWithExperimentalFeatures(` at line 2065:

```go
func (s *moduleSourceSchema) moduleSourceExperimentalFeatureEnabled(
	_ context.Context,
	parentSrc *core.ModuleSource,
	args struct {
		Feature core.ModuleSourceExperimentalFeature
	},
) (dagql.Boolean, error) {
	if parentSrc.SDK == nil {
		return false, nil
	}
	return dagql.Boolean(parentSrc.SDK.ExperimentalFeatureEnabled(args.Feature)), nil
}

```

(Keep the blank line between this and the next function.)

### Step 3: Verify the schema compiles

- [ ] **Run `go build ./core/schema/...`**

Expected: exit 0, no compile errors.

```bash
go build ./core/schema/...
```

### Step 4: Run existing modulesource schema tests to catch accidental regression

- [ ] **Run `go test ./core/schema/... -run ModuleSource -count=1 -timeout 60s`**

Expected: PASS (tests exercise setters; our additive getter does not change any existing behavior).

```bash
go test ./core/schema/... -run ModuleSource -count=1 -timeout 60s
```

### Step 5: Smoke-test the new field via the GraphQL schema

- [ ] **Build the engine and introspect the schema**

Expected: the introspection output contains `experimentalFeatureEnabled` on `ModuleSource`.

```bash
go run ./cmd/introspect introspect | jq '.__schema.types[] | select(.name=="ModuleSource") | .fields[] | select(.name=="experimentalFeatureEnabled") | .name'
```

Expected output:
```
"experimentalFeatureEnabled"
```

### Step 6: Commit as stg patch

- [ ] **Stage and commit**

```bash
stg new python-sdk-schematool-experimental-getter -m "$(cat <<'EOF'
core/schema: experimentalFeatureEnabled getter on ModuleSource

Add a read-only dagql field so SDK modules (not the engine core) can
query whether a given experimental feature is enabled on a module
source. Needed by the Python SDK runtime to branch codegen on the
SELF_CALLS feature — without a getter it would have to round-trip the
flag through some other side channel.

Additive only: existing setters (withExperimentalFeatures /
withoutExperimentalFeatures) are untouched.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

Expected: the new patch is on top of the stack.

---

## Task 2: `_analyzer` emit CLI + schematool serializer + Python unit tests

**Files:**
- Create: `sdk/python/src/dagger/mod/_analyzer/schematool.py`
- Create: `sdk/python/src/dagger/mod/_analyzer/__main__.py`
- Create: `sdk/python/tests/mod/test_schematool_serializer.py`

### Step 1: Write the serializer test file

- [ ] **Create `sdk/python/tests/mod/test_schematool_serializer.py`**

```python
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
```

### Step 2: Run the tests to confirm they fail

- [ ] **Run the serializer tests**

Expected: ImportError / ModuleNotFoundError for `dagger.mod._analyzer.schematool`.

```bash
cd sdk/python && uv run --frozen pytest tests/mod/test_schematool_serializer.py -v
```

Expected output:
```
ModuleNotFoundError: No module named 'dagger.mod._analyzer.schematool'
```

### Step 3: Implement the serializer

- [ ] **Create `sdk/python/src/dagger/mod/_analyzer/schematool.py`**

```python
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
        if obj.name not in base_type_names
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
    out: dict[str, Any] = {
        "name": field.api_name,
        "type": _resolved_to_typeref(field.resolved_type),
    }
    if field.doc:
        out["description"] = field.doc
    return out


def _function_to_dict(fn: FunctionMetadata) -> dict[str, Any]:
    out: dict[str, Any] = {
        "name": fn.api_name,
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
    type_ref = _resolved_to_typeref(param.resolved_type, nullable=param.is_optional)
    out: dict[str, Any] = {
        "name": param.api_name,
        "type": type_ref,
    }
    if param.doc:
        out["description"] = param.doc
    if param.has_default:
        out["defaultValue"] = _render_default(param.default_value)
    return out


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
```

### Step 4: Run the tests to verify they pass

- [ ] **Run `uv run --frozen pytest tests/mod/test_schematool_serializer.py -v`**

Expected: all six tests PASS.

```bash
cd sdk/python && uv run --frozen pytest tests/mod/test_schematool_serializer.py -v
```

### Step 5: Write the CLI test

- [ ] **Append to `sdk/python/tests/mod/test_schematool_serializer.py`**

```python


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
```

### Step 6: Run the CLI test — confirm failure

- [ ] **Run `uv run --frozen pytest tests/mod/test_schematool_serializer.py::test_cli_emits_schematool_json -v`**

Expected: FAIL with `No module named dagger.mod._analyzer.__main__` (or similar argparse/invocation error).

```bash
cd sdk/python && uv run --frozen pytest tests/mod/test_schematool_serializer.py::test_cli_emits_schematool_json -v
```

### Step 7: Implement the CLI entry point

- [ ] **Create `sdk/python/src/dagger/mod/_analyzer/__main__.py`**

```python
"""CLI for the AST-based module analyzer.

    python -m dagger.mod._analyzer emit \\
        --module-source-dir <dir> \\
        --main-object <ClassName> \\
        --module-name <name> \\
        --introspection-json <schema.json> \\
        --output <module-types.json>

Writes a schematool.ModuleTypes JSON document. Consumed by
``cmd/codegen merge-schema`` during Python codegen when the
``SELF_CALLS`` experimental feature is enabled.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
from pathlib import Path

from dagger.mod._analyzer.analyze import analyze_module
from dagger.mod._analyzer.errors import AnalysisError, ParseError, ValidationError
from dagger.mod._analyzer.schematool import to_schematool_json


def _find_source_files(root: Path) -> list[str]:
    """Walk ``root`` for .py files, skipping private (``_``-prefixed) dirs.

    Mirrors ``dagger.mod._discovery._collect_package_files`` semantics
    closely enough for the analyzer (sorted, __init__.py last within a
    directory).
    """
    files: list[str] = []

    def _walk(pkg_path: Path) -> None:
        init_file: Path | None = None
        for py_file in sorted(pkg_path.glob("*.py")):
            if py_file.name == "__init__.py":
                init_file = py_file
            else:
                files.append(str(py_file))
        for subdir in sorted(pkg_path.iterdir()):
            if subdir.is_dir() and not subdir.name.startswith("_"):
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
    except (OSError, json.JSONDecodeError):
        return frozenset()
    schema = payload.get("__schema", payload)
    types = schema.get("types", []) if isinstance(schema, dict) else []
    return frozenset(
        t["name"] for t in types
        if isinstance(t, dict) and isinstance(t.get("name"), str)
    )


def _emit(args: argparse.Namespace) -> int:
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

    if not source_files:
        # Empty module (e.g. greenfield ``dagger init`` before the
        # template is rendered) — emit an empty ModuleTypes.
        payload: dict[str, object] = {
            "name": module_name,
            "objects": [],
            "enums": [],
        }
    else:
        try:
            metadata = analyze_module(
                source_files=source_files,
                main_object_name=main_object,
                module_name=module_name,
            )
        except (AnalysisError, ParseError, ValidationError) as exc:
            sys.stderr.write(f"analyzer: {exc}\n")
            return 1
        base_types = _load_base_type_names(
            Path(args.introspection_json) if args.introspection_json else None,
        )
        payload = to_schematool_json(metadata, base_types)

    encoded = json.dumps(payload, indent=2)
    if args.output:
        Path(args.output).write_text(encoded, encoding="utf-8")
    else:
        sys.stdout.write(encoded)
        sys.stdout.write("\n")
    return 0


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(level=os.environ.get("DAGGER_LOG_LEVEL", "WARNING"))

    parser = argparse.ArgumentParser(
        prog="python -m dagger.mod._analyzer",
        description="AST-based type analyzer for Dagger Python modules.",
    )
    sub = parser.add_subparsers(dest="cmd", required=True)

    emit = sub.add_parser(
        "emit",
        help="Emit schematool.ModuleTypes JSON for a module's source tree.",
    )
    emit.add_argument(
        "--module-source-dir",
        required=True,
        help="Root of the user's Python package.",
    )
    emit.add_argument(
        "--main-object",
        help="Main object class name (defaults to $DAGGER_MAIN_OBJECT).",
    )
    emit.add_argument(
        "--module-name",
        help="Module name (defaults to $DAGGER_MODULE).",
    )
    emit.add_argument(
        "--introspection-json",
        help="Path to the base introspection schema JSON.",
    )
    emit.add_argument(
        "--output",
        help="Write JSON here instead of stdout.",
    )
    emit.set_defaults(func=_emit)

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
```

### Step 8: Run the full Python unit test module — confirm everything passes

- [ ] **Run `uv run --frozen pytest tests/mod/test_schematool_serializer.py -v`**

Expected: all seven tests PASS.

```bash
cd sdk/python && uv run --frozen pytest tests/mod/test_schematool_serializer.py -v
```

### Step 9: Run the rest of the Python SDK unit tests to confirm no collateral damage

- [ ] **Run `uv run --frozen pytest tests/mod/ -q`**

Expected: existing analyzer tests still pass; no new failures.

```bash
cd sdk/python && uv run --frozen pytest tests/mod/ -q
```

### Step 10: Commit as stg patch

- [ ] **Stage new files and commit**

```bash
stg add sdk/python/src/dagger/mod/_analyzer/schematool.py
stg add sdk/python/src/dagger/mod/_analyzer/__main__.py
stg add sdk/python/tests/mod/test_schematool_serializer.py
stg new python-sdk-analyzer-emit-cli -m "$(cat <<'EOF'
sdk(python): add _analyzer emit CLI + schematool serializer

Expose the AST analyzer as a CLI that produces the language-agnostic
schematool.ModuleTypes JSON consumed by cmd/codegen merge-schema. The
serializer filters out types that already exist in the base
introspection schema so the downstream merge does not reject the
payload on name collisions.

  python -m dagger.mod._analyzer emit \
      --module-source-dir <dir> \
      --main-object <ClassName> \
      --module-name <name> \
      --introspection-json <schema.json> \
      --output <module-types.json>

Unit tests cover objects, enums, lists, optional parameters, private
attributes, base-schema references, and the end-to-end CLI.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

---

## Task 3: Bundle `merge-schema` in the Python SDK rootfs

**Files:**
- Modify: `toolchains/engine-dev/build/sdk.go::pythonSDKContent` (around line 98, inside the `rootfs = rootfs.` chain)

### Step 1: Locate the insertion point

- [ ] **Confirm the existing `WithFile("dist/codegen", codegenShiv)` line**

```bash
grep -n 'WithFile("dist/codegen"' toolchains/engine-dev/build/sdk.go
```

Expected output includes line 98.

### Step 2: Add the merge-schema bundle

- [ ] **Edit `toolchains/engine-dev/build/sdk.go`**

Locate:

```go
	rootfs = rootfs.
		// bundle the codegen script and its dependencies into a single executable
		WithFile("dist/codegen", codegenShiv).
```

Replace with:

```go
	rootfs = rootfs.
		// bundle the codegen script and its dependencies into a single executable
		WithFile("dist/codegen", codegenShiv).
		// bundle cmd/codegen as `merge-schema`: sdk/python/runtime calls
		// its `merge-schema` subcommand when SELF_CALLS is enabled, to
		// extend the introspection schema with the module's declared
		// types before invoking the Python codegen generator.
		WithFile("dist/merge-schema", build.CodegenBinary()).
```

### Step 3: Build the engine image to confirm the change compiles

- [ ] **Run the engine-dev build**

```bash
go build ./toolchains/engine-dev/...
```

Expected: exit 0.

### Step 4: Commit as stg patch

- [ ] **Commit**

```bash
stg new python-sdk-bundle-merge-schema -m "$(cat <<'EOF'
engine-dev: bundle merge-schema in python-sdk rootfs

Ship cmd/codegen as dist/merge-schema next to dist/codegen in the
python-sdk rootfs. sdk/python/runtime/main.go::WithSDK will mount it
and invoke the `merge-schema` subcommand between analyze and generate
when SELF_CALLS is enabled on the module source.

Reuses the existing cmd/codegen binary (it already has the
merge-schema subcommand from PR 1), so there is no new Go binary to
build.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

---

## Task 4: Self-calls gated three-phase codegen in `WithSDK`

**Files:**
- Modify: `sdk/python/runtime/main.go` (add field, populate in `Load`, branch in `WithSDK`)
- Regenerate: `sdk/python/runtime/internal/dagger/dagger.gen.go` (via `dagger develop`)

### Step 1: Add `SelfCallsEnabled` field to `PythonSdk`

- [ ] **Edit `sdk/python/runtime/main.go`**

Locate the struct field list (around line 139):

```go
	// Discovery holds the logic for getting more information from the target module.
	// +private
	Discovery *Discovery
}
```

Insert before the closing brace `}`:

```go
	// SelfCallsEnabled is true when the module source has the
	// SELF_CALLS experimental feature turned on (i.e. the user ran
	// `dagger init --with-self-calls` or `dagger develop --with-self-calls`).
	// When true, WithSDK runs the three-phase analyze -> merge -> generate
	// pipeline so gen.py contains bindings for the module's declared types.
	// +private
	SelfCallsEnabled bool
```

### Step 2: Populate `SelfCallsEnabled` in `Load`

- [ ] **Edit `Load` in `sdk/python/runtime/main.go`**

Locate (around line 244):

```go
func (m *PythonSdk) Load(ctx context.Context, modSource *dagger.ModuleSource) (*PythonSdk, error) {
	m.ModSource = modSource
	m.ContextDir = modSource.ContextDirectory()
	debug, err := modSource.SDK().Debug(ctx)
	if err != nil {
		return nil, fmt.Errorf("runtime module load: %w", err)
	}
	m.Debug = debug
```

After `m.Debug = debug`, insert:

```go
	selfCalls, err := modSource.ExperimentalFeatureEnabled(
		ctx, dagger.ModuleSourceExperimentalFeatureSelfCalls,
	)
	if err != nil {
		return nil, fmt.Errorf("runtime module load: check self-calls feature: %w", err)
	}
	m.SelfCallsEnabled = selfCalls
```

(Exact enum constant name — `ModuleSourceExperimentalFeatureSelfCalls` — and method signature — `ExperimentalFeatureEnabled(ctx, feature)` — come from the generated `internal/dagger/dagger.gen.go` after Task 1 lands and `dagger develop` regenerates. Step 4 below handles regeneration.)

### Step 3: Rewrite `WithSDK` to branch on self-calls

- [ ] **Edit `WithSDK` in `sdk/python/runtime/main.go`**

Replace the current body (the existing `if introspectionJSON != nil { … }` block, roughly lines 389-434) with:

```go
	// Allow empty introspection to facilitate debugging the container with a
	// `dagger call module-runtime terminal` command.
	if introspectionJSON != nil {
		var genFile *dagger.File

		if m.SelfCallsEnabled {
			genFile = m.runSelfCallsCodegen(introspectionJSON)
		} else {
			// The builtin engine ships a prebuilt gen.py at .dagger-build/gen.py,
			// produced from this engine's schema at image build time (see
			// toolchains/engine-dev/build/sdk.go::pythonGenPy). When present it
			// is byte-identical to what codegen would emit for introspectionJSON
			// from the same engine, so we skip the codegen exec entirely and
			// save ~2.8s per cold Python module load.
			if m.Discovery.SdkHasFile(".dagger-build/gen.py") {
				genFile = m.SdkSourceDir.File(".dagger-build/gen.py")
			} else {
				genFile = m.runPlainCodegen(introspectionJSON)
			}
		}

		genPath := UserGenPath

		// For now, patch vendored client library with generated bindings.
		// TODO: Always generate outside library, even if vendored.
		if m.VendorPath != "" {
			genPath = path.Join(m.VendorPath, SDKGenPath)
		}

		m.AddFile(genPath, genFile)
	}

	return m
}

// runPlainCodegen runs the Python codegen binary directly on the base
// introspection schema, producing a gen.py with only upstream types.
// This is today's behavior — used when SELF_CALLS is off.
func (m *PythonSdk) runPlainCodegen(introspectionJSON *dagger.File) *dagger.File {
	ctr := m.Container
	cmd := []string{"codegen"}

	if m.Discovery.SdkHasFile("dist/codegen") {
		ctr = ctr.
			WithMountedCache("/root/.shiv", dag.CacheVolume("shiv")).
			WithMountedFile("/usr/local/bin/codegen", m.SdkSourceDir.File("dist/codegen"))
	} else {
		ctr = ctr.
			WithWorkdir("/sdk").
			WithMountedDirectory("", m.SdkSourceDir)
		cmd = []string{
			"uv", "run", "--isolated", "--frozen", "--package", "codegen",
			"python", "-m", "codegen",
		}
	}

	return ctr.
		WithMountedFile(SchemaPath, introspectionJSON).
		WithExec(append(cmd, "generate", "-i", SchemaPath, "-o", "/gen.py")).
		File("/gen.py")
}

// runSelfCallsCodegen runs the three-phase pipeline:
//  1. python -m dagger.mod._analyzer emit  -> /module-types.json
//  2. merge-schema                         -> /extended-schema.json
//  3. codegen generate                     -> /gen.py
// Invoked when the module source has the SELF_CALLS experimental
// feature enabled.
func (m *PythonSdk) runSelfCallsCodegen(introspectionJSON *dagger.File) *dagger.File {
	userSourceDir := path.Join(m.ContextDirPath, m.SubPath, "src", m.PackageName)

	ctr := m.Container.
		WithMountedFile(SchemaPath, introspectionJSON).
		WithMountedDirectory(m.ContextDirPath, m.ContextDir).
		WithMountedDirectory("/sdk", m.SdkSourceDir).
		WithWorkdir("/sdk").
		WithMountedFile(
			"/usr/local/bin/merge-schema",
			m.SdkSourceDir.File("dist/merge-schema"),
		)

	// Phase 1: analyze
	ctr = ctr.WithExec([]string{
		"uv", "run", "--isolated", "--frozen", "--package", "dagger",
		"python", "-m", "dagger.mod._analyzer", "emit",
		"--module-source-dir", userSourceDir,
		"--main-object", m.MainObjectName,
		"--module-name", m.ModName,
		"--introspection-json", SchemaPath,
		"--output", "/module-types.json",
	})

	// Phase 2: merge
	ctr = ctr.WithExec([]string{
		"merge-schema",
		"--introspection-json-path", SchemaPath,
		"--module-types-path", "/module-types.json",
		"--output-path", "/extended-schema.json",
	})

	// Phase 3: generate (reuse shiv-or-uv-run for codegen)
	var codegenCmd []string
	if m.Discovery.SdkHasFile("dist/codegen") {
		ctr = ctr.
			WithMountedCache("/root/.shiv", dag.CacheVolume("shiv")).
			WithMountedFile("/usr/local/bin/codegen", m.SdkSourceDir.File("dist/codegen"))
		codegenCmd = []string{"codegen"}
	} else {
		codegenCmd = []string{
			"uv", "run", "--isolated", "--frozen", "--package", "codegen",
			"python", "-m", "codegen",
		}
	}

	return ctr.
		WithExec(append(codegenCmd, "generate", "-i", "/extended-schema.json", "-o", "/gen.py")).
		File("/gen.py")
}
```

Note: the closing `}` of `WithSDK` in the original code and the `return m` at the top of the old block must still be present. Keep the signature `func (m *PythonSdk) WithSDK(introspectionJSON *dagger.File) *PythonSdk {` unchanged.

### Step 4: Regenerate `dagger.gen.go` + `internal/dagger/*`

- [ ] **Run `dagger develop` against the python runtime**

The Go-side runtime is itself a Dagger module. Regenerate so `dagger.ModuleSource` exposes `ExperimentalFeatureEnabled` (added in Task 1).

```bash
hack/dev dagger -m sdk/python/runtime develop
```

Expected: `sdk/python/runtime/dagger.gen.go` and `sdk/python/runtime/internal/dagger/dagger.gen.go` are updated; the generated client has an `ExperimentalFeatureEnabled` method on `*dagger.ModuleSource` and a `ModuleSourceExperimentalFeatureSelfCalls` constant.

Verify:

```bash
grep -n "ExperimentalFeatureEnabled" sdk/python/runtime/internal/dagger/dagger.gen.go | head -3
grep -n "ModuleSourceExperimentalFeatureSelfCalls" sdk/python/runtime/internal/dagger/dagger.gen.go | head -3
```

Expected: both matches present.

### Step 5: Compile the Python SDK runtime

- [ ] **Run `go build` inside `sdk/python/runtime`**

```bash
cd sdk/python/runtime && go build ./... && cd -
```

Expected: exit 0.

### Step 6: Smoke-test with the engine-dev playground — self-calls OFF path

- [ ] **Launch the playground and init a Python module without --with-self-calls**

```bash
bash skills/engine-dev-testing/with-playground.sh "
cd /work &&
mkdir py-off &&
cd py-off &&
dagger init --sdk=python --name=test --source=. &&
dagger functions
"
```

Expected: `dagger functions` succeeds and lists the default `container-echo` function. No crashes from the new `SelfCallsEnabled` path.

### Step 7: Smoke-test with the playground — self-calls ON path (baseline, without user self-reference)

- [ ] **Init with --with-self-calls and run `dagger functions`**

```bash
bash skills/engine-dev-testing/with-playground.sh "
cd /work &&
mkdir py-on &&
cd py-on &&
dagger init --sdk=python --name=test --source=. --with-self-calls &&
dagger functions
"
```

Expected: succeeds. Three phases run; extended schema is consumed by codegen; no errors.

### Step 8: Defer full self-reference validation to Task 5

Verifying the end-to-end self-calls story (a module calling
`dag.test().container_echo()` from within itself) requires writing
multi-line Python source into the playground sandbox, which is
awkward with shell heredocs. The existing `TestSelfCalls/python`
integration test already encodes exactly this scenario and is
enabled in Task 5 Step 1. Do not attempt a manual smoke test here —
Steps 6 and 7 above are sufficient to confirm the runtime driver
compiles, loads, and branches correctly; Task 5 provides the
end-to-end gate.

### Step 9: Commit as stg patch

- [ ] **Commit**

```bash
stg new python-sdk-self-calls-codegen -m "$(cat <<'EOF'
sdk(python/runtime): self-calls gated three-phase codegen

When the module source has the SELF_CALLS experimental feature on,
WithSDK now orchestrates a three-phase codegen container:

  1. analyze: python -m dagger.mod._analyzer emit -> /module-types.json
  2. merge:   merge-schema -> /extended-schema.json
  3. generate: codegen generate -> /gen.py

gen.py therefore contains bindings for the module's own declared
types, so `dag.<mod>.<SelfType>()` self-calls from within the module
resolve correctly.

When SELF_CALLS is off, codegen runs plainly against the base schema
(today's behavior), including the prebuilt .dagger-build/gen.py
short-circuit from the engine-dev rootfs. No regression for the common
case.

Gate propagates via the new moduleSource.experimentalFeatureEnabled
dagql getter through the regenerated sdk/python/runtime client
bindings.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -3
```

---

## Task 5: Integration tests — enable Python case, add self-calls-off regression

**Files:**
- Modify: `core/integration/module_test.go::TestSelfCalls` (uncomment lines 6446-6464)
- Modify: same file, add new `TestSelfCallsOff` test below `TestSelfCalls`

### Step 1: Uncomment the Python case in `TestSelfCalls`

- [ ] **Edit `core/integration/module_test.go`**

Locate lines 6445-6464 (the commented-out Python block, immediately after the commented TypeScript block):

```go
		//		{
		//			sdk: "python",
		//			source: `import dagger
		// from dagger import dag, function, object_type
		//
		// @object_type
		// class Test:
		//     @function
		//     def container_echo(self, string_arg: str = "Hello Self Calls") -> dagger.Container:
		//         return dag.container().from_("alpine:latest").with_exec(["echo", string_arg])
		//
		//     @function
		//     async def print(self, string_arg: str) -> str:
		//         return await dag.test().container_echo(string_arg=string_arg).stdout()
		//
		//     @function
		//     async def print_default(self) -> str:
		//         return await dag.test().container_echo().stdout()
		// `,
		//		},
```

Replace with (uncomment, preserving the exact Go raw-string syntax):

```go
		{
			sdk: "python",
			source: `import dagger
from dagger import dag, function, object_type

@object_type
class Test:
    @function
    def container_echo(self, string_arg: str = "Hello Self Calls") -> dagger.Container:
        return dag.container().from_("alpine:latest").with_exec(["echo", string_arg])

    @function
    async def print(self, string_arg: str) -> str:
        return await dag.test().container_echo(string_arg=string_arg).stdout()

    @function
    async def print_default(self) -> str:
        return await dag.test().container_echo().stdout()
`,
		},
```

### Step 2: Add the self-calls-off regression test

- [ ] **Append a new test below `TestSelfCalls`**

After the closing brace of `TestSelfCalls` (currently around line 6489) and before `TestGoCodegenPhase1Parity` (line 6500), insert:

```go
// TestSelfCallsOffPython verifies that a Python module initialized
// WITHOUT --with-self-calls does not pay the three-phase
// analyze+merge+generate cost and its generated gen.py does not
// contain bindings for the module's own declared types. This pins the
// default-off behavior so the SELF_CALLS gate cannot silently regress.
func (ModuleSuite) TestSelfCallsOffPython(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)

	source := `import dagger
from dagger import dag, function, object_type

@object_type
class Test:
    @function
    def hello(self) -> str:
        return "hi"
`

	modGen := modInit(t, c, "python", source) // NOTE: no --with-self-calls

	t.Run("module calls still work", func(ctx context.Context, t *testctx.T) {
		out, err := modGen.
			With(daggerQuery(`{hello}`)).
			Stdout(ctx)
		require.NoError(t, err)
		require.JSONEq(t, `{"hello":"hi"}`, out)
	})

	t.Run("gen.py does not contain the module's self type", func(ctx context.Context, t *testctx.T) {
		// Trigger codegen materialization and read the generated
		// vendored gen.py. Module-declared types (like `class Test`)
		// must NOT appear when SELF_CALLS is off.
		gen, err := modGen.
			File("sdk/src/dagger/client/gen.py").
			Contents(ctx)
		require.NoError(t, err)
		require.NotContains(t, gen, "class Test(",
			"gen.py contains the user-declared Test class but "+
				"--with-self-calls was not passed")
	})
}
```

### Step 3: Run the updated `TestSelfCalls` suite (Python case)

- [ ] **Run the test**

```bash
go test ./core/integration/ -run 'TestModuleSuite/TestSelfCalls/python' -count=1 -timeout 15m -v
```

Expected: all three sub-tests (`can call with arguments`, `can call with optional arguments`) PASS.

### Step 4: Run the self-calls-off regression test

- [ ] **Run the test**

```bash
go test ./core/integration/ -run 'TestModuleSuite/TestSelfCallsOffPython' -count=1 -timeout 10m -v
```

Expected: both sub-tests PASS.

### Step 5: Run the Go case to confirm no regression there

- [ ] **Run the test**

```bash
go test ./core/integration/ -run 'TestModuleSuite/TestSelfCalls/go' -count=1 -timeout 15m -v
```

Expected: PASS (untouched path).

### Step 6: Commit as stg patch

- [ ] **Commit**

```bash
stg new python-sdk-self-calls-integration-tests -m "$(cat <<'EOF'
core/integration: enable python case in TestSelfCalls

Uncomment the Python test case that was parked behind the
"self-calls doesn't work for Python yet" comment. The three-phase
analyze/merge/generate codegen pipeline added in this stack makes
dag.test().container_echo() resolve correctly from within the module.

Also add TestSelfCallsOffPython to pin the default-off behavior: a
Python module initialized without --with-self-calls runs plain
codegen and gen.py does not contain bindings for the module's own
declared types.

Signed-off-by: Yves Brissaud <yves@dagger.io>
EOF
)"
stg refresh
stg series --all | tail -5
```

---

## Final verification

### Step 1: Run the full TestSelfCalls suite

- [ ] **Go + Python cases**

```bash
go test ./core/integration/ -run 'TestModuleSuite/TestSelfCalls' -count=1 -timeout 20m -v
```

Expected: both sub-SDKs pass.

### Step 2: Confirm the full Python unit test suite passes

- [ ] **All analyzer tests**

```bash
cd sdk/python && uv run --frozen pytest tests/mod/ -q && cd -
```

Expected: PASS.

### Step 3: Confirm the stack is clean

- [ ] **Inspect stg state**

```bash
stg series --all
git status --porcelain
```

Expected: the top of the stack is `python-sdk-self-calls-integration-tests`, working tree clean, five new patches on top of the previous stack top (`python-sdk-schematool-codegen-design`).

---

## Out-of-scope reminders (do not implement in this plan)

- **Spec 2**: "Python SDK: static runtime entrypoint" — analysis moves from runtime to codegen, generated `entrypoint.py` for static dispatch. Always-on.
- **Spec 3**: "Python SDK: honor `legacyCodegenAtRuntime`" — opt-in to skip codegen at runtime by committing generated files.
- **`dist/analyzer` shiv bundle** (prod parity with `dist/codegen`): nice-to-have follow-up; `uv run` path is used for now.
- **Replacing `ModuleTypesExp --register` with the AST analyzer**: separate optimization independent of self-calls.
