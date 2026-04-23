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
        parent_state = json.loads((await fn_call.parent()) or "{}") or {}
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
