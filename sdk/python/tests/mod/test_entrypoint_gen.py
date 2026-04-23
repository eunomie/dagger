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


def test_nullable_dagger_object_param_guards_none():
    """A nullable (Optional) Dagger-object parameter must guard the
    coercion call; passing None to dag.directory_from_id would crash."""
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Test:
            @dagger.function
            def upload(self, src: dagger.Directory | None = None) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")
    assert "if _raw is not None else None" in src
    assert "coerce_id(dag.directory_from_id, _raw)" in src


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


def test_list_of_user_object_param_emits_coercer_and_import():
    """A list-of-user-object parameter needs both the coerce_list call
    and a `from <package> import <UserObject>` inside the case arm so
    the inner lambda can reference the class.
    """
    md = _analyze("""
        import dagger

        @dagger.object_type
        class Foo:
            name: str = dagger.field()

        @dagger.object_type
        class Test:
            @dagger.function
            def many(self, items: list[Foo]) -> str: ...
    """)

    src = generate_entrypoint(md, package="mypkg")
    # The emitted body must import Foo AND call coerce_list(lambda x: coerce_object(Foo, x), ...).
    assert "from mypkg import Foo" in src
    assert 'coerce_list(lambda x: coerce_object(Foo, x), inputs["items"])' in src


def test_deprecated_and_default_path_annotations_are_emitted():
    """Dagger annotations (deprecated, default_path, default_address,
    ignore) on functions / args must appear in the generated source so
    the registered schema matches the dynamic path.
    """
    md = _analyze("""
        import dagger
        from typing import Annotated

        @dagger.object_type
        class Test:
            @dagger.function
            def echo(
                self,
                msg: Annotated[str, dagger.Deprecated("use newEcho")] = "hi",
            ) -> str: ...
    """)

    src = generate_entrypoint(md, package="main")
    assert 'deprecated="use newEcho"' in src


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
