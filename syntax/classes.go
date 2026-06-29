// Copyright 2026 fsedano fork. Python-style class support for Starlark.
// Use of this source code is governed by a BSD-style license.

package syntax

import (
	"fmt"
	"strconv"
)

// This file lowers ("desugars") the fork's `class` and `@decorator` syntax into
// ordinary Starlark constructs, so that the resolver, compiler, and runtime VM
// need no awareness of classes. Lowering runs at the end of parsing (see
// FileOptions.Parse), only when FileOptions.Classes is set.
//
// A class
//
//	@deco
//	class Dog(Animal):
//	    kind = "canine"
//	    def __init__(self, name): self.name = name
//	    @staticmethod
//	    def family(): return "Canidae"
//
// becomes, conceptually,
//
//	def $class1():
//	    def __init__(self, name): self.name = name
//	    def family(): return "Canidae"
//	    family = staticmethod(family)
//	    kind = "canine"
//	    return {"__init__": __init__, "family": family, "kind": kind}
//	Dog = deco($make_class("Dog", (Animal,), $class1()))
//
// `$make_class` (and `staticmethod`/`classmethod`/`super`) are predeclared
// universal builtins provided by the consumer via starlark.Universe; the
// `$`-prefixed names cannot collide with user identifiers.
//
// Position invariant: every synthetic node is stamped with a real source
// Position (the `class` keyword, the bound name, or the decorator). Method and
// class-variable bodies are reused verbatim, so they retain their original
// positions and runtime tracebacks report accurate file:line.

// desugarClasses rewrites all ClassStmt nodes and decorated def/class nodes in
// the file in place. After it returns, the tree contains no *ClassStmt and no
// DefStmt/ClassStmt with a non-empty Decorators field.
func desugarClasses(f *File) {
	d := &desugarer{}
	f.Stmts = d.stmts(f.Stmts)
}

type desugarer struct {
	n int // monotonic counter for unique synthetic builder names (per file)
}

// stmts lowers a statement list, expanding each statement into zero or more
// replacement statements.
func (d *desugarer) stmts(in []Stmt) []Stmt {
	if in == nil {
		return nil
	}
	out := make([]Stmt, 0, len(in))
	for _, s := range in {
		out = append(out, d.stmt(s)...)
	}
	return out
}

func (d *desugarer) stmt(s Stmt) []Stmt {
	switch s := s.(type) {
	case *ClassStmt:
		return d.class(s)

	case *DefStmt:
		s.Body = d.stmts(s.Body)
		if len(s.Decorators) == 0 {
			return []Stmt{s}
		}
		decs := s.Decorators
		s.Decorators = nil
		return []Stmt{s, applyDecorators(s.Name, decs)}

	case *IfStmt:
		s.True = d.stmts(s.True)
		s.False = d.stmts(s.False)
		return []Stmt{s}

	case *ForStmt:
		s.Body = d.stmts(s.Body)
		return []Stmt{s}

	case *WhileStmt:
		s.Body = d.stmts(s.Body)
		return []Stmt{s}

	default:
		return []Stmt{s}
	}
}

// class lowers a single ClassStmt into a builder def plus the class-binding
// assignment. The body has already been validated by the parser
// (parseClassStmt) to contain only methods, simple class-var assignments,
// nested classes, `pass`, and bare expressions.
func (d *desugarer) class(c *ClassStmt) []Stmt {
	pos := c.Class

	// Collect member names in source order (defs, class-var assigns, nested
	// classes), de-duplicated so a redefinition keeps a single namespace key.
	var members []string
	seen := map[string]bool{}
	addMember := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			members = append(members, name)
		}
	}
	for _, s := range c.Body {
		switch s := s.(type) {
		case *DefStmt:
			addMember(s.Name.Name)
		case *ClassStmt:
			addMember(s.Name.Name)
		case *AssignStmt:
			if id, ok := s.LHS.(*Ident); ok {
				addMember(id.Name)
			}
		}
	}

	// Builder function: executes the (recursively lowered) class body and
	// returns the namespace dict.
	d.n++
	buildName := fmt.Sprintf("$class%d", d.n)
	body := d.stmts(c.Body)
	body = append(body, &ReturnStmt{Return: pos, Result: namespaceDict(members, pos)})
	builder := &DefStmt{
		Def:    pos,
		Name:   clsIdent(buildName, pos),
		Lparen: pos,
		Rparen: pos,
		Body:   body,
	}

	// Name = [decorators...]($make_class("Name", (bases,), $classN()))
	var rhs Expr = &CallExpr{
		Fn:     clsIdent("$make_class", pos),
		Lparen: pos,
		Args: []Expr{
			clsString(c.Name.Name, pos),
			&TupleExpr{Lparen: pos, List: c.Bases, Rparen: pos},
			&CallExpr{Fn: clsIdent(buildName, pos), Lparen: pos, Rparen: pos},
		},
		Rparen: pos,
	}
	for i := len(c.Decorators) - 1; i >= 0; i-- {
		rhs = &CallExpr{Fn: c.Decorators[i], Lparen: pos, Args: []Expr{rhs}, Rparen: pos}
	}
	assign := &AssignStmt{
		OpPos: c.Name.NamePos,
		Op:    EQ,
		LHS:   clsIdent(c.Name.Name, c.Name.NamePos),
		RHS:   rhs,
	}

	return []Stmt{builder, assign}
}

// applyDecorators builds `name = d1(d2(... name ...))` for a decorated def,
// with decorators applied outermost-first (matching Python).
func applyDecorators(name *Ident, decs []Expr) *AssignStmt {
	pos := name.NamePos
	var rhs Expr = clsIdent(name.Name, pos)
	for i := len(decs) - 1; i >= 0; i-- {
		rhs = &CallExpr{Fn: decs[i], Lparen: pos, Args: []Expr{rhs}, Rparen: pos}
	}
	return &AssignStmt{
		OpPos: pos,
		Op:    EQ,
		LHS:   clsIdent(name.Name, pos),
		RHS:   rhs,
	}
}

// namespaceDict builds {"m": m, ...} referencing the builder-local bindings.
func namespaceDict(members []string, pos Position) *DictExpr {
	entries := make([]Expr, len(members))
	for i, m := range members {
		entries[i] = &DictEntry{
			Key:   clsString(m, pos),
			Colon: pos,
			Value: clsIdent(m, pos),
		}
	}
	return &DictExpr{Lbrace: pos, List: entries, Rbrace: pos}
}

func clsIdent(name string, pos Position) *Ident {
	return &Ident{NamePos: pos, Name: name}
}

func clsString(s string, pos Position) *Literal {
	return &Literal{Token: STRING, TokenPos: pos, Raw: strconv.Quote(s), Value: s}
}
