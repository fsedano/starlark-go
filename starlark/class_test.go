// Copyright 2026 fsedano fork. Runtime tests for Python-style classes.

package starlark_test

import (
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func execClasses(t *testing.T, src string) (starlark.StringDict, error) {
	t.Helper()
	thread := &starlark.Thread{Name: "test", Print: func(*starlark.Thread, string) {}}
	opts := &syntax.FileOptions{Classes: true}
	return starlark.ExecFileOptions(opts, thread, "test.star", src, nil)
}

func wantStr(t *testing.T, g starlark.StringDict, name, want string) {
	t.Helper()
	v, ok := g[name]
	if !ok {
		t.Fatalf("global %q not set", name)
	}
	s, ok := starlark.AsString(v)
	if !ok {
		t.Fatalf("global %q = %s, want string", name, v.Type())
	}
	if s != want {
		t.Errorf("global %q = %q, want %q", name, s, want)
	}
}

func wantTrue(t *testing.T, g starlark.StringDict, name string) {
	t.Helper()
	if v, ok := g[name]; !ok || v != starlark.True {
		t.Errorf("global %q = %v, want True", name, g[name])
	}
}

func TestClassBasics(t *testing.T) {
	g, err := execClasses(t, `
class Animal:
    kind = "animal"
    def __init__(self, name):
        self.name = name
    def describe(self):
        return self.name + " the " + self.kind

a = Animal("Rex")
desc = a.describe()
classvar = Animal.kind
field = a.name
`)
	if err != nil {
		t.Fatal(err)
	}
	wantStr(t, g, "desc", "Rex the animal")
	wantStr(t, g, "classvar", "animal")
	wantStr(t, g, "field", "Rex")
}

func TestClassInheritanceSuper(t *testing.T) {
	g, err := execClasses(t, `
class Animal:
    def __init__(self, name):
        self.name = name
    def describe(self):
        return self.name + " the animal"

class Dog(Animal):
    def describe(self):
        return super(Dog, self).describe() + " (dog)"

d = Dog("Rex")
desc = d.describe()
is_animal = isinstance(d, Animal)
is_dog = isinstance(d, Dog)
not_inst = isinstance(42, Dog)
`)
	if err != nil {
		t.Fatal(err)
	}
	wantStr(t, g, "desc", "Rex the animal (dog)")
	wantTrue(t, g, "is_animal")
	wantTrue(t, g, "is_dog")
	if g["not_inst"] != starlark.False {
		t.Errorf("isinstance(42, Dog) = %v, want False", g["not_inst"])
	}
}

func TestStaticAndClassMethods(t *testing.T) {
	g, err := execClasses(t, `
class C:
    tag = "C"
    @staticmethod
    def add(a, b):
        return a + b
    @classmethod
    def make(cls):
        return cls()

s = C.add(2, 3)
made = C.make()
made_tag = made.tag
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := g["s"]; got != starlark.MakeInt(5) {
		t.Errorf("C.add(2,3) = %v, want 5", got)
	}
	wantStr(t, g, "made_tag", "C")
}

// TestDiamondMRO validates C3 linearization (multiple inheritance): D(B, C)
// where B, C both derive from A must resolve who() to B.
func TestDiamondMRO(t *testing.T) {
	g, err := execClasses(t, `
class A:
    def who(self): return "A"
class B(A):
    def who(self): return "B"
class C(A):
    def who(self): return "C"
class D(B, C):
    pass

w = D().who()
`)
	if err != nil {
		t.Fatal(err)
	}
	wantStr(t, g, "w", "B")
}

// TestMethodErrorBacktrace is the debuggability guarantee: an error raised
// inside a method body reports the precise original source line, and the
// traceback names the method.
func TestMethodErrorBacktrace(t *testing.T) {
	_, err := execClasses(t, `
class Calc:
    def div(self, x):
        return x / 0

c = Calc()
c.div(5)
`)
	if err == nil {
		t.Fatal("expected a division-by-zero error")
	}
	evalErr, ok := err.(*starlark.EvalError)
	if !ok {
		t.Fatalf("err = %T, want *starlark.EvalError", err)
	}
	bt := evalErr.Backtrace()
	// The division is on line 4 of the source.
	if !strings.Contains(bt, "test.star:4") {
		t.Errorf("backtrace does not point at the real error line (test.star:4):\n%s", bt)
	}
	// The method name appears in the traceback.
	if !strings.Contains(bt, "div") {
		t.Errorf("backtrace does not name the method 'div':\n%s", bt)
	}
}

func TestDuplicateBase(t *testing.T) {
	_, err := execClasses(t, `
class A:
    pass
class B(A, A):
    pass
`)
	if err == nil || !strings.Contains(err.Error(), "duplicate base class") {
		t.Fatalf("err = %v, want a duplicate-base-class error", err)
	}
}

func TestUnhashableInstance(t *testing.T) {
	_, err := execClasses(t, `
class C:
    pass
d = {C(): 1}
`)
	if err == nil || !strings.Contains(err.Error(), "unhashable") {
		t.Fatalf("err = %v, want an unhashable-instance error", err)
	}
}

func TestZeroArgSuper(t *testing.T) {
	g, err := execClasses(t, `
class A:
    def __init__(self, x):
        self.x = x
    def who(self):
        return "A"

class B(A):
    def __init__(self, x):
        super().__init__(x + 1)
    def who(self):
        return "B>" + super().who()

class C(B):
    def who(self):
        return "C>" + super().who()

c = C(10)
chain = c.who()
xval = c.x
`)
	if err != nil {
		t.Fatal(err)
	}
	// Nested zero-arg super() must walk C->B->A via the per-call context stack.
	wantStr(t, g, "chain", "C>B>A")
	if g["xval"] != starlark.MakeInt(11) {
		t.Errorf("c.x = %v, want 11 (super().__init__ chain)", g["xval"])
	}
}

func TestZeroArgSuperOutsideMethod(t *testing.T) {
	_, err := execClasses(t, "x = super()\n")
	if err == nil || !strings.Contains(err.Error(), "no enclosing method") {
		t.Fatalf("err = %v, want 'no enclosing method' error", err)
	}
}
