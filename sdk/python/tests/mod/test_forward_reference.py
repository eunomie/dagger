import pytest
from typing_extensions import Self

from dagger.mod import Module

pytestmark = pytest.mark.skip(
    reason=(
        "Dynamic __dagger_module__ dispatch replaced by codegen'd "
        "_dagger_main.py static entrypoint (spec 2). AST-path coverage "
        "lives in test_ast_analyzer.py; entrypoint coverage lives in "
        "test_entrypoint_gen.py."
    )
)

mod = Module()


@mod.object_type
class Foo:
    @mod.function
    def method1(self) -> "Foo": ...

    @mod.function
    def method2(self) -> Self: ...


def test_method_returns_resolved_forward_reference():
    fn = mod.get_object("Foo").functions["method1"]
    assert fn.return_type is Foo


def test_method_returns_resolved_self():
    fn = mod.get_object("Foo").functions["method2"]
    assert fn.return_type is Foo
