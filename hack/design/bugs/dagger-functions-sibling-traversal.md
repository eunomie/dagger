# Bug Fix: `dagger functions` cannot traverse into sibling workspace modules

## Status: Implemented

## Problem

In a workspace with a blueprint module, `dagger functions python-sdk` fails:

```
Error: no function "python-sdk" in type "DaggerDev"
```

While `dagger call python-sdk --help` works correctly.

The root cause: `dagger functions` starts traversal from `MainObject` (the blueprint's
object, e.g. `DaggerDev`). Sibling workspace modules like `python-sdk` are not functions
on that object — they're Query-root functions from other modules. The traversal loop in
`funcListCmd` only searches the current function provider, so it can't find them.

The **display** already works: `siblingModuleEntrypoints()` is called at the top level to
include siblings in the listing. Only the **traversal** is broken.

`dagger call` avoids this because it builds a cobra command tree that explicitly includes
sibling commands via `addSiblingModuleCommands()`.

## Fix

### Change: Expand traversal to check sibling entrypoints (call.go)

In the traversal loop (`call.go:82-104`), when the first step fails to find a function on
`MainObject`, fall back to checking sibling module entrypoints.

Only the first step needs this: siblings are only visible at the top level, matching the
display behavior.

```go
for i, field := range functionPath {
    nextFunc, err := GetSupportedFunction(mod, o, field)
    if err != nil {
        // On the first step, the field may refer to a sibling workspace
        // module rather than a function on the default module's object.
        if i == 0 {
            if sf := findSiblingEntrypoint(mod, field); sf != nil {
                nextFunc = sf
                err = nil
            }
        }
        if err != nil {
            return err
        }
    }
    nextType := nextFunc.ReturnType
    if nextType.AsFunctionProvider() != nil {
        o = mod.GetFunctionProvider(nextType.Name())
        continue
    }
    return fmt.Errorf(...)
}
```

### Helper: findSiblingEntrypoint (call.go or module_inspect.go)

```go
func findSiblingEntrypoint(mod *moduleDef, name string) *modFunction {
    for _, fn := range mod.siblingModuleEntrypoints() {
        if fn.Name == name || fn.CmdName() == name {
            mod.LoadFunctionTypeDefs(fn)
            return fn
        }
    }
    return nil
}
```

### Why this works

Once we resolve the sibling function, its return type (e.g. `PythonSdk`) is already in
`mod.Objects` because `loadTypeDefs` populates all loaded modules' types. The existing
`mod.GetFunctionProvider(nextType.Name())` call finds it, and further traversal steps
(e.g. `dagger functions python-sdk python-310`) work naturally.

## Bonus: Selective module loading

Currently `ensureModulesLoaded` resolves all workspace modules in parallel on first query.
An optimization could defer loading non-blueprint, non-targeted modules until needed. This
is orthogonal to the bug fix and should be designed separately.

## Optimization: Selective module loading

When a function path is provided (e.g. `dagger functions python-sdk` or
`dagger call python-sdk --help`), the CLI already knows the first function name. Only
blueprint modules and the targeted module need to be loaded — not the entire workspace.

### Change 1: Thread focus module from CLI to engine

The CLI already extracts the first function name via `functionName()` into
`Params.Function`. Thread it to the engine:

- `engine/opts.go`: Add `FocusModule string` to `ClientMetadata`
- `engine/client/client.go`: Set `md.FocusModule = c.Function` when building metadata

### Change 2: Filter in gatherModuleLoadRequests (engine/server/session.go)

When `FocusModule` is set, skip workspace config modules that are neither blueprints nor
matching the focus name:

```go
func gatherModuleLoadRequests(pending []pendingModule, extras []engine.ExtraModule, focusModule string) []moduleLoadRequest {
    for _, mod := range pending {
        if focusModule != "" && !mod.Blueprint && mod.Name != focusModule {
            continue
        }
        // ...
    }
    // extras always load (explicit -m flag)
}
```

### Result

For the dagger/dagger workspace (~20 modules), `dagger functions python-sdk` goes from
loading all modules to loading only 2 (blueprint + python-sdk).
