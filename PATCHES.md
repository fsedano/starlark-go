# Fork patches (fsedano/starlark-go)

This fork adds Python-style `class` support to the Starlark dialect, for the
aes-gw2 gateway. All divergence from upstream is **confined to the `syntax/`
package** so re-applying after an upstream bump is mechanical. The class runtime
(MRO, instances, super, static/class methods) lives in the *consumer* repo and
registers builtins via the exported `starlark.Universe`, so `resolve/`,
`internal/compile/`, and `starlark/` are intentionally left untouched.

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

## Invariants

- The `Classes` flag is **off by default**; only the consumer enables it. Stock
  Starlark programs parse and behave identically when off.
- Desugar runs inside `ParseOptions` so both execution (`SourceProgram`) and the
  editor's bare `syntax.Parse` validation see consistent behavior.
- No synthetic AST node ships a zero `Position` (tested).
