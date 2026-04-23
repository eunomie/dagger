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
