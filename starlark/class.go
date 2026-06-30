// Copyright 2026 fsedano fork. Python-style class runtime for Starlark.
// Use of this source code is governed by a BSD-style license.
//
// This file implements the runtime half of the fork's `class` feature. The
// syntax half (parsing + desugaring of `class`/`@decorator`) lives in the
// syntax package; it lowers a class into ordinary defs plus a call to the
// predeclared `$make_class` builtin (see classbuiltins.go), which returns a
// *Class defined here. Nothing outside this file and classbuiltins.go is
// class-aware: the resolver, compiler, and VM are untouched.
//
// Supported: instance methods + self, __init__, single & multiple inheritance
// with a C3-linearized MRO, super(Cls, self), class variables, and
// @staticmethod / @classmethod. Not supported (by design): operator/dunder
// overloading and @property/descriptors.

package starlark

import (
	"fmt"
	"sort"

	"go.starlark.net/syntax"
)

// A Class is a user-defined Starlark class produced by a `class` statement.
// It is callable (calling it constructs an Instance and runs __init__) and
// exposes its methods and class variables as attributes.
type Class struct {
	name    string
	bases   []*Class
	mro     []*Class   // C3 linearization; mro[0] == this class
	members StringDict // methods, class vars, and static/class-method markers
	frozen  bool
}

var (
	_ Value    = (*Class)(nil)
	_ Callable = (*Class)(nil)
	_ HasAttrs = (*Class)(nil)
)

// Name returns the class name (also its Callable name).
func (c *Class) Name() string   { return c.name }
func (c *Class) String() string { return fmt.Sprintf("<class %q>", c.name) }
func (c *Class) Type() string   { return "class" }
func (c *Class) Truth() Bool    { return True }

func (c *Class) Freeze() {
	if !c.frozen {
		c.frozen = true
		for _, v := range c.members {
			v.Freeze()
		}
	}
}

// Hash makes classes usable as dict keys / set members, keyed by name. Two
// distinct classes that share a name may collide, which is permitted.
func (c *Class) Hash() (uint32, error) { return String(c.name).Hash() }

// lookup returns the named member and the class that defines it, searching the
// MRO. The defining class seeds zero-arg super() and bound-method dispatch.
func (c *Class) lookup(name string) (Value, *Class) {
	for _, k := range c.mro {
		if v, ok := k.members[name]; ok {
			return v, k
		}
	}
	return nil, nil
}

func (c *Class) Attr(name string) (Value, error) {
	if raw, dc := c.lookup(name); raw != nil {
		return bindMember(raw, nil, c, dc)
	}
	return nil, nil
}

func (c *Class) AttrNames() []string { return memberNames(c, nil) }

func (c *Class) CallInternal(thread *Thread, args Tuple, kwargs []Tuple) (Value, error) {
	inst := &Instance{class: c, fields: make(map[string]Value)}
	if init, dc := c.lookup("__init__"); init != nil {
		fn, ok := init.(Callable)
		if !ok {
			return nil, fmt.Errorf("%s.__init__ is not callable (%s)", c.name, init.Type())
		}
		if _, err := Call(thread, &boundMethod{recv: inst, fn: fn, cls: c.name, defining: dc}, args, kwargs); err != nil {
			return nil, err
		}
	} else if len(args) > 0 || len(kwargs) > 0 {
		return nil, fmt.Errorf("%s() takes no arguments (it has no __init__)", c.name)
	}
	return inst, nil
}

// An Instance is an object produced by calling a Class.
type Instance struct {
	class  *Class
	fields map[string]Value
	frozen bool
}

var (
	_ Value       = (*Instance)(nil)
	_ HasAttrs    = (*Instance)(nil)
	_ HasSetField = (*Instance)(nil)
)

func (i *Instance) String() string { return fmt.Sprintf("<%s object>", i.class.name) }

// Type returns the class name, so error messages read naturally and
// type(x) reports the class.
func (i *Instance) Type() string { return i.class.name }
func (i *Instance) Truth() Bool  { return True }

func (i *Instance) Freeze() {
	if !i.frozen {
		i.frozen = true
		for _, v := range i.fields {
			v.Freeze()
		}
	}
}

// Hash reports instances as unhashable by default (no __hash__ in this dialect),
// which also keeps program execution deterministic.
func (i *Instance) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: %s", i.class.name)
}

func (i *Instance) Attr(name string) (Value, error) {
	if v, ok := i.fields[name]; ok {
		return v, nil
	}
	if raw, dc := i.class.lookup(name); raw != nil {
		return bindMember(raw, i, i.class, dc)
	}
	return nil, nil
}

func (i *Instance) AttrNames() []string { return memberNames(i.class, i) }

// SetField writes an instance attribute (self.x = v).
func (i *Instance) SetField(name string, v Value) error {
	if i.frozen {
		return fmt.Errorf("cannot set .%s field of frozen %s", name, i.class.name)
	}
	i.fields[name] = v
	return nil
}

// bindMember adapts a raw member: inst is the receiver (nil through the class),
// recvClass binds @classmethod, defining seeds zero-arg super().
func bindMember(raw Value, inst *Instance, recvClass, defining *Class) (Value, error) {
	switch m := raw.(type) {
	case *staticMethod:
		return m.fn, nil
	case *classMethod:
		fn, ok := m.fn.(Callable)
		if !ok {
			return nil, fmt.Errorf("classmethod wraps a non-callable %s", m.fn.Type())
		}
		return &boundMethod{recv: recvClass, fn: fn, cls: recvClass.name, defining: defining}, nil
	case Callable:
		if inst != nil {
			return &boundMethod{recv: inst, fn: m, cls: recvClass.name, defining: defining}, nil
		}
		return m, nil
	default:
		return raw, nil
	}
}

// memberNames returns the sorted union of MRO member names and (if inst != nil)
// instance field names, for dir().
func memberNames(c *Class, inst *Instance) []string {
	seen := map[string]bool{}
	for _, k := range c.mro {
		for name := range k.members {
			seen[name] = true
		}
	}
	if inst != nil {
		for name := range inst.fields {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// boundMethod delegates through Call so the function gets its own VM frame,
// keeping tracebacks pointed at the real method source line.
type boundMethod struct {
	recv     Value
	fn       Callable
	cls      string
	defining *Class // resolves zero-arg super() to the method's owning class
}

var (
	_ Value                = (*boundMethod)(nil)
	_ Callable             = (*boundMethod)(nil)
	_ callableWithPosition = (*boundMethod)(nil)
)

func (b *boundMethod) Name() string   { return b.cls + "." + b.fn.Name() }
func (b *boundMethod) String() string { return fmt.Sprintf("<bound method %s>", b.Name()) }
func (b *boundMethod) Type() string   { return "bound_method" }
func (b *boundMethod) Truth() Bool    { return True }
func (b *boundMethod) Freeze()        { b.recv.Freeze(); b.fn.Freeze() }
func (b *boundMethod) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: bound_method")
}

// Position points the boundMethod's own frame at the method definition, so the
// extra dispatch frame in a traceback is informative rather than blank. The
// underlying *Function frame still carries the precise error line.
func (b *boundMethod) Position() syntax.Position {
	if p, ok := b.fn.(callableWithPosition); ok {
		return p.Position()
	}
	return syntax.MakePosition(&builtinFilename, 0, 0)
}

func (b *boundMethod) CallInternal(thread *Thread, args Tuple, kwargs []Tuple) (Value, error) {
	if b.defining != nil {
		pushSuperContext(thread, b.defining, b.recv)
		defer popSuperContext(thread)
	}
	all := make(Tuple, 0, len(args)+1)
	all = append(all, b.recv)
	all = append(all, args...)
	return Call(thread, b.fn, all, kwargs)
}

// superContext records the executing method's owning class and receiver so a
// zero-arg super() resolves like super(class, recv). A per-thread stack keeps
// it correct across nested method calls.
type superContext struct {
	class *Class
	recv  Value
}

const superContextKey = "starlark.class.super"

func pushSuperContext(thread *Thread, class *Class, recv Value) {
	stack, _ := thread.Local(superContextKey).([]superContext)
	thread.SetLocal(superContextKey, append(stack, superContext{class, recv}))
}

func popSuperContext(thread *Thread) {
	stack, _ := thread.Local(superContextKey).([]superContext)
	if n := len(stack); n > 0 {
		thread.SetLocal(superContextKey, stack[:n-1])
	}
}

func currentSuperContext(thread *Thread) (*Class, Value, bool) {
	stack, _ := thread.Local(superContextKey).([]superContext)
	if len(stack) == 0 {
		return nil, nil, false
	}
	top := stack[len(stack)-1]
	return top.class, top.recv, true
}

// A superProxy implements super(Cls, obj): attribute lookups resolve in the
// receiver's MRO starting just after Cls.
type superProxy struct {
	start    *Class    // search resumes after this class in the MRO
	inst     *Instance // receiver to bind methods to
	objClass *Class    // the receiver's class, providing the MRO
}

var (
	_ Value    = (*superProxy)(nil)
	_ HasAttrs = (*superProxy)(nil)
)

func (s *superProxy) String() string { return fmt.Sprintf("<super: %s>", s.objClass.name) }
func (s *superProxy) Type() string   { return "super" }
func (s *superProxy) Truth() Bool    { return True }
func (s *superProxy) Freeze()        {}
func (s *superProxy) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: super")
}

func (s *superProxy) Attr(name string) (Value, error) {
	mro := s.objClass.mro
	i := 0
	for ; i < len(mro); i++ {
		if mro[i] == s.start {
			break
		}
	}
	for _, k := range mro[i+1:] {
		if raw, ok := k.members[name]; ok {
			return bindMember(raw, s.inst, s.objClass, k)
		}
	}
	return nil, nil
}

func (s *superProxy) AttrNames() []string {
	seen := map[string]bool{}
	mro := s.objClass.mro
	i := 0
	for ; i < len(mro); i++ {
		if mro[i] == s.start {
			break
		}
	}
	for _, k := range mro[i+1:] {
		for name := range k.members {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// staticMethod / classMethod mark a function inside a class namespace. They are
// transient wrappers produced by the staticmethod()/classmethod() builtins and
// unwrapped during attribute access.
type staticMethod struct{ fn Value }
type classMethod struct{ fn Value }

var (
	_ Value = (*staticMethod)(nil)
	_ Value = (*classMethod)(nil)
)

func (m *staticMethod) String() string        { return "<staticmethod>" }
func (m *staticMethod) Type() string          { return "staticmethod" }
func (m *staticMethod) Truth() Bool           { return True }
func (m *staticMethod) Freeze()               { m.fn.Freeze() }
func (m *staticMethod) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: staticmethod") }

func (m *classMethod) String() string        { return "<classmethod>" }
func (m *classMethod) Type() string          { return "classmethod" }
func (m *classMethod) Truth() Bool           { return True }
func (m *classMethod) Freeze()               { m.fn.Freeze() }
func (m *classMethod) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: classmethod") }

// linearize computes the C3 MRO for c, whose bases already have their mro set.
func linearize(c *Class) ([]*Class, error) {
	var seqs [][]*Class
	for _, b := range c.bases {
		seqs = append(seqs, append([]*Class(nil), b.mro...))
	}
	if len(c.bases) > 0 {
		seqs = append(seqs, append([]*Class(nil), c.bases...))
	}
	merged, err := c3merge(seqs)
	if err != nil {
		return nil, fmt.Errorf("cannot create a consistent method resolution order for class %s: %w", c.name, err)
	}
	return append([]*Class{c}, merged...), nil
}

func c3merge(seqs [][]*Class) ([]*Class, error) {
	var result []*Class
	for {
		// Drop exhausted sequences.
		live := seqs[:0]
		for _, s := range seqs {
			if len(s) > 0 {
				live = append(live, s)
			}
		}
		seqs = live
		if len(seqs) == 0 {
			return result, nil
		}
		// Pick the first head that does not appear in the tail of any sequence.
		var cand *Class
		for _, s := range seqs {
			head := s[0]
			if !inTail(head, seqs) {
				cand = head
				break
			}
		}
		if cand == nil {
			return nil, fmt.Errorf("inconsistent hierarchy")
		}
		result = append(result, cand)
		for i := range seqs {
			if len(seqs[i]) > 0 && seqs[i][0] == cand {
				seqs[i] = seqs[i][1:]
			}
		}
	}
}

func inTail(c *Class, seqs [][]*Class) bool {
	for _, s := range seqs {
		for _, x := range s[1:] {
			if x == c {
				return true
			}
		}
	}
	return false
}
