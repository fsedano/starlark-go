// Copyright 2026 fsedano fork. Builtins backing the class runtime.
// Use of this source code is governed by a BSD-style license.
//
// These builtins are registered into Universe (see library.go) so that
// consumers need only enable FileOptions.Classes; no per-thread wiring is
// required. `$make_class` is synthetic (emitted by the desugarer; its name is
// unspellable in user code); super/isinstance/staticmethod/classmethod are
// user-facing.

package starlark

import "fmt"

// makeClass implements the synthetic `$make_class(name, bases, namespace)`
// call that the syntax desugarer emits for every `class` statement.
func makeClass(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) != 0 {
		return nil, fmt.Errorf("$make_class: unexpected keyword arguments")
	}
	if len(args) != 3 {
		return nil, fmt.Errorf("$make_class: got %d arguments, want 3", len(args))
	}
	nameStr, ok := args[0].(String)
	if !ok {
		return nil, fmt.Errorf("$make_class: name must be a string, got %s", args[0].Type())
	}
	name := string(nameStr)
	basesTuple, ok := args[1].(Tuple)
	if !ok {
		return nil, fmt.Errorf("$make_class: bases must be a tuple, got %s", args[1].Type())
	}
	ns, ok := args[2].(*Dict)
	if !ok {
		return nil, fmt.Errorf("$make_class: namespace must be a dict, got %s", args[2].Type())
	}

	var bases []*Class
	for _, bv := range basesTuple {
		bc, ok := bv.(*Class)
		if !ok {
			return nil, fmt.Errorf("base of class %s must be a class, got %s", name, bv.Type())
		}
		bases = append(bases, bc)
	}

	members := make(StringDict, ns.Len())
	for _, item := range ns.Items() {
		key, ok := item[0].(String)
		if !ok {
			return nil, fmt.Errorf("class %s: member name must be a string", name)
		}
		members[string(key)] = item[1]
	}

	c := &Class{name: name, bases: bases, members: members}
	mro, err := linearize(c)
	if err != nil {
		return nil, err
	}
	c.mro = mro
	return c, nil
}

// super implements super(Cls, obj): method lookups resolve in obj's MRO
// starting just after Cls. (Explicit two-argument form; the zero-argument
// Python 3 form is a possible future addition.)
func super(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var clsV, objV Value
	if err := UnpackPositionalArgs("super", args, kwargs, 2, &clsV, &objV); err != nil {
		return nil, err
	}
	cls, ok := clsV.(*Class)
	if !ok {
		return nil, fmt.Errorf("super: first argument must be a class, got %s", clsV.Type())
	}
	inst, ok := objV.(*Instance)
	if !ok {
		return nil, fmt.Errorf("super: second argument must be an instance, got %s", objV.Type())
	}
	found := false
	for _, k := range inst.class.mro {
		if k == cls {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("super(%s, obj): %s is not in the MRO of %s", cls.name, cls.name, inst.class.name)
	}
	return &superProxy{start: cls, inst: inst, objClass: inst.class}, nil
}

// isinstance reports whether obj is an instance of class (or of any class in a
// tuple of classes), consulting the MRO.
func isinstance(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var objV, classinfo Value
	if err := UnpackPositionalArgs("isinstance", args, kwargs, 2, &objV, &classinfo); err != nil {
		return nil, err
	}
	inst, ok := objV.(*Instance)
	if !ok {
		return False, nil
	}
	match := func(target Value) (bool, error) {
		c, ok := target.(*Class)
		if !ok {
			return false, fmt.Errorf("isinstance: second argument must be a class or tuple of classes, got %s", target.Type())
		}
		for _, k := range inst.class.mro {
			if k == c {
				return true, nil
			}
		}
		return false, nil
	}
	if tup, ok := classinfo.(Tuple); ok {
		for _, t := range tup {
			m, err := match(t)
			if err != nil {
				return nil, err
			}
			if m {
				return True, nil
			}
		}
		return False, nil
	}
	m, err := match(classinfo)
	if err != nil {
		return nil, err
	}
	return Bool(m), nil
}

func staticmethod(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var fn Value
	if err := UnpackPositionalArgs("staticmethod", args, kwargs, 1, &fn); err != nil {
		return nil, err
	}
	return &staticMethod{fn: fn}, nil
}

func classmethod(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var fn Value
	if err := UnpackPositionalArgs("classmethod", args, kwargs, 1, &fn); err != nil {
		return nil, err
	}
	return &classMethod{fn: fn}, nil
}
