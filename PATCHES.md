# Fork patches (fsedano/starlark-go)

This fork adds Python-style `class` support to the Starlark dialect, for the
aes-gw2 gateway (and any other consumer — flight sims, etc.). It is a complete,
self-contained unit: enabling `FileOptions.Classes` is all a consumer does, with
no per-thread wiring.

Divergence from upstream lives in two layers, each adding **mostly new files**
so re-applying after an upstream bump is mechanical:
- `syntax/` — the `class`/`@decorator` syntax, lowered (desugared) at parse time
  into ordinary defs + a `$make_class(...)` call. `resolve/` and
  `internal/compile/` stay completely untouched.
- `starlark/` — the class runtime (Class, Instance, MRO, super, static/class
  methods) as new files, plus five entries added to the `Universe` literal in
  `library.go`.

Track for review: design doc lives in the consumer repo at
`dev_notes/starlark_classes_plan.md`.

## Touched files

| File | Change | Status |
|---|---|---|
| `syntax/options.go` | Add `FileOptions.Classes bool` flag (gates the new syntax; zero-value = off, so stock behavior is unchanged). | DONE |
| `syntax/scan.go` | `class` already scanned (reserved keyword); added `AT` (`@`) token for decorators. | DONE |
| `syntax/syntax.go` | Added `ClassStmt` AST node + `DefStmt.Decorators`/`ClassStmt.Decorators` fields. | DONE |
| `syntax/walk.go` | `Walk` cases for `ClassStmt` and decorator traversal. | DONE |
| `syntax/parse.go` | `parseClassStmt` + `parseDecorated`; dispatch (gated on `Classes`) in `parseStmt`; class-body validation; desugar hook in `Parse`. | DONE |
| `syntax/classes.go` | New file: the desugar pass — lowers `ClassStmt` → `$classN()` builder + `$make_class(...)`; `@dec def f` → `def f; f = dec(f)`. Every synthetic node copies a real source `Position` (line-number invariant). | DONE |
| `syntax/classes_test.go` | New file: desugar-shape, decorators, member order, position invariant, flag-off stock behavior, class-body validation. | DONE |
| `starlark/class.go` | New file: `Class`, `Instance`, `boundMethod`, `superProxy`, `staticMethod`/`classMethod` markers, C3 MRO, attribute binding. | DONE |
| `starlark/classbuiltins.go` | New file: `$make_class`, `super`, `isinstance`, `staticmethod`, `classmethod` builtins. | DONE |
| `starlark/library.go` | Added the five class builtins to the `Universe` literal. | DONE |
| `starlark/class_test.go` | New file: instantiation, self, inheritance, super, static/class methods, C3 diamond MRO, unhashable instances, and the method-error backtrace (line-number) guarantee. | DONE |

## Invariants

- The `Classes` flag is **off by default**; only the consumer enables it. Stock
  Starlark programs parse and behave identically when off. (The runtime builtins
  do become universal names, but they do not change parsing and are shadowable;
  the full upstream `syntax` and `starlark` test suites pass.)
- Desugar runs inside `Parse` (the `FileOptions.Parse` method) so both execution
  (`SourceProgram`) and the editor's bare `syntax.Parse` validation see
  consistent behavior.
- No synthetic AST node ships a zero `Position` (tested).
- Errors inside method bodies report the precise original `file:line:col`;
  method bodies are reused verbatim by the desugarer, and `boundMethod`
  delegates through `Call` so the underlying function gets a real VM frame
  (tested via backtrace assertions).
