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
