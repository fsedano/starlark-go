// Copyright 2026 fsedano fork. Tests for Python-style class desugaring.

package syntax_test

import (
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

func parseClasses(t *testing.T, src string) *syntax.File {
	t.Helper()
	f, err := (&syntax.FileOptions{Classes: true}).Parse("test.star", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	return f
}

// requireValidPositions asserts the desugarer's invariant: no node in the tree
// carries an invalid (zero) start position, so tracebacks and resolve errors
// report accurate file:line.
func requireValidPositions(t *testing.T, f *syntax.File) {
	t.Helper()
	syntax.Walk(f, func(n syntax.Node) bool {
		if n == nil {
			return false
		}
		if start := syntax.Start(n); !start.IsValid() {
			t.Errorf("node %T has invalid start position", n)
		}
		return true
	})
}

func TestClassDesugarShape(t *testing.T) {
	f := parseClasses(t, `
class Dog(Animal):
    kind = "canine"
    def speak(self):
        return "woof"
`)
	// One class -> builder def + binding assignment.
	if got := len(f.Stmts); got != 2 {
		t.Fatalf("got %d top-level stmts, want 2: %#v", got, f.Stmts)
	}

	builder, ok := f.Stmts[0].(*syntax.DefStmt)
	if !ok {
		t.Fatalf("stmt[0] = %T, want *DefStmt (builder)", f.Stmts[0])
	}
	if !strings.HasPrefix(builder.Name.Name, "$class") {
		t.Errorf("builder name = %q, want $class* prefix", builder.Name.Name)
	}

	assign, ok := f.Stmts[1].(*syntax.AssignStmt)
	if !ok {
		t.Fatalf("stmt[1] = %T, want *AssignStmt (class binding)", f.Stmts[1])
	}
	lhs, ok := assign.LHS.(*syntax.Ident)
	if !ok || lhs.Name != "Dog" {
		t.Fatalf("assign LHS = %#v, want Ident Dog", assign.LHS)
	}
	// The bound name keeps its ORIGINAL source position (line 2 here).
	if lhs.NamePos.Line != 2 {
		t.Errorf("Dog binding NamePos.Line = %d, want 2 (original)", lhs.NamePos.Line)
	}

	call, ok := assign.RHS.(*syntax.CallExpr)
	if !ok {
		t.Fatalf("assign RHS = %T, want *CallExpr", assign.RHS)
	}
	fn, ok := call.Fn.(*syntax.Ident)
	if !ok || fn.Name != "$make_class" {
		t.Fatalf("call Fn = %#v, want Ident $make_class", call.Fn)
	}
	if len(call.Args) != 3 {
		t.Fatalf("$make_class got %d args, want 3 (name, bases, namespace)", len(call.Args))
	}
	// arg0: class name literal
	if lit, ok := call.Args[0].(*syntax.Literal); !ok || lit.Value != "Dog" {
		t.Errorf("arg0 = %#v, want string literal \"Dog\"", call.Args[0])
	}
	// arg1: bases tuple containing Animal
	bases, ok := call.Args[1].(*syntax.TupleExpr)
	if !ok || len(bases.List) != 1 {
		t.Fatalf("arg1 = %#v, want 1-element bases tuple", call.Args[1])
	}
	if base, ok := bases.List[0].(*syntax.Ident); !ok || base.Name != "Animal" {
		t.Errorf("base = %#v, want Ident Animal", bases.List[0])
	}
	// arg2: builder invocation $classN()
	if _, ok := call.Args[2].(*syntax.CallExpr); !ok {
		t.Errorf("arg2 = %#v, want builder call", call.Args[2])
	}

	requireValidPositions(t, f)
}

func TestClassNoBases(t *testing.T) {
	f := parseClasses(t, "class Empty:\n    pass\n")
	assign := f.Stmts[1].(*syntax.AssignStmt)
	call := assign.RHS.(*syntax.CallExpr)
	bases := call.Args[1].(*syntax.TupleExpr)
	if len(bases.List) != 0 {
		t.Errorf("bases = %#v, want empty tuple", bases.List)
	}
	requireValidPositions(t, f)
}

func TestDecoratedDef(t *testing.T) {
	f := parseClasses(t, `
@staticmethod
def f():
    return 1
`)
	if len(f.Stmts) != 2 {
		t.Fatalf("got %d stmts, want 2 (def + assign)", len(f.Stmts))
	}
	def, ok := f.Stmts[0].(*syntax.DefStmt)
	if !ok || def.Name.Name != "f" {
		t.Fatalf("stmt[0] = %#v, want def f", f.Stmts[0])
	}
	if def.Decorators != nil {
		t.Errorf("def.Decorators = %#v, want nil after lowering", def.Decorators)
	}
	assign, ok := f.Stmts[1].(*syntax.AssignStmt)
	if !ok {
		t.Fatalf("stmt[1] = %T, want assignment", f.Stmts[1])
	}
	call, ok := assign.RHS.(*syntax.CallExpr)
	if !ok {
		t.Fatalf("RHS = %T, want call", assign.RHS)
	}
	if fn, ok := call.Fn.(*syntax.Ident); !ok || fn.Name != "staticmethod" {
		t.Errorf("decorator fn = %#v, want staticmethod", call.Fn)
	}
	requireValidPositions(t, f)
}

func TestClassMemberOrder(t *testing.T) {
	f := parseClasses(t, `
class C:
    a = 1
    def m(self):
        return self.a
    b = 2
`)
	builder := f.Stmts[0].(*syntax.DefStmt)
	ret := builder.Body[len(builder.Body)-1].(*syntax.ReturnStmt)
	dict := ret.Result.(*syntax.DictExpr)
	var keys []string
	for _, e := range dict.List {
		keys = append(keys, e.(*syntax.DictEntry).Key.(*syntax.Literal).Value.(string))
	}
	want := []string{"a", "m", "b"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("namespace keys = %v, want %v", keys, want)
	}
	requireValidPositions(t, f)
}

// Stock behavior: with Classes off, `class` is rejected exactly as before.
func TestClassesDisabled(t *testing.T) {
	_, err := (&syntax.FileOptions{}).Parse("test.star", "class Foo:\n    pass\n", 0)
	if err == nil {
		t.Fatal("expected parse error for class when Classes option is off")
	}
}

func TestClassBodyValidation(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"for loop", "class C:\n    for i in [1]:\n        pass\n", "unsupported statement in class body"},
		{"augmented assign", "class C:\n    x = 0\n    x += 1\n", "augmented assignment"},
		{"tuple target", "class C:\n    a, b = 1, 2\n", "single name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&syntax.FileOptions{Classes: true}).Parse("test.star", tc.src, 0)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
