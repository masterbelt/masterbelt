// Package eval is the value half of masterbelt's constant analysis: it folds
// the resolved IR value graph to its value (ir.Constant) — the interpreter in
// graph.go and its statement-execution half in graph_body.go. Where package
// types reasons about an expression's type, eval derives its value, over the
// same resolved graph: a Reference is bound to its declaration, a call to its
// selection, every implicit conversion to an explicit Adapt — so a fold needs
// nothing but the IR and the builtin registry's native table.
//
// Evaluation reads referenced values through a GraphEnv, so it has no
// dependency on the semantic query engine: the engine supplies a memoizing
// environment (which also tracks dependencies and guards cycles), but the
// rules here are a pure function of the graph and that environment. This file
// holds the value-level helpers the interpreter shares: the collection-mapness
// channel, the union-member kind backing, and the constant readings.
package eval

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/builtin"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// CollKindOf returns the mapness a resolved type names — CollMap for a map<K,V>
// (or a nominal type whose underlying is a map), CollList for a list<T> (or a
// nominal list), and CollUnknown for anything else. It is the channel an empty
// collection literal settles through, derived from a declared type. A union is
// deliberately left CollUnknown: a member being a collection does not pin
// which kind the literal is, so the literal stays undecided rather than being
// settled wrong.
func CollKindOf(want ir.Type) ir.CollKind {
	switch w := want.(type) {
	case *ir.App:
		if w.Def == nil {
			return ir.CollUnknown
		}
		switch w.Def.Name {
		case builtin.NameMap:
			return ir.CollMap
		case builtin.NameList:
			return ir.CollList
		}
		return collKindFromDef(w.Def)
	case *ir.Named:
		return collKindFromDef(w.Def)
	}
	return ir.CollUnknown
}

// collKindFromDef returns the mapness a nominal type bottoms out at — a Bag (=
// list<int>) yields CollList — by following its underlying collection
// definition, or CollUnknown for a type with no list/map underlying.
func collKindFromDef(def *ir.TypeDef) ir.CollKind {
	d := underlyingCollectionDef(def, map[*ir.TypeDef]bool{})
	if d == nil {
		return ir.CollUnknown
	}
	switch d.Name {
	case builtin.NameMap:
		return ir.CollMap
	case builtin.NameList:
		return ir.CollList
	}
	return ir.CollUnknown
}

// maxApplyDepth caps function-application recursion: a recursive fold that has
// not bottomed out by this depth is treated as unevaluable (nil) — the same
// verdict an engine-level value cycle gets — instead of overflowing the stack.
const maxApplyDepth = 256

// maxRangeIterations caps the number of elements a range fold or for visits at
// compile time. A range is constructed lazily from its bounds, so range(0,
// 1_000_000_000) is a small value; only walking it would materialize the
// sequence. Folding or iterating a range wider than this bound is treated as
// unevaluable — the same conservative verdict the depth guard gives — so a
// wide range neither hangs the folder nor exhausts memory. It is a
// compile-time evaluation limit, not a language limit.
const maxRangeIterations = 1 << 20

// recordField returns the value of a record constant's named field, or nil when
// the record has no such field (a malformed program the checker reports). It is
// how a field access (p.lv) reads its value once the record has folded.
func recordField(recv *ir.Constant, name string) *ir.Constant {
	for _, f := range recv.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return nil
}

// refinedMemberDef returns the definition behind a nominal (or applied) member
// type when it carries a usable where-clause, or nil — what the member
// admission runs the predicate of.
func refinedMemberDef(t ir.Type) *ir.TypeDef {
	var def *ir.TypeDef
	switch t := t.(type) {
	case *ir.Named:
		def = t.Def
	case *ir.App:
		def = t.Def
	}
	if def == nil || def.Where == nil {
		return nil
	}
	return def
}

// defType returns the type a definition denotes for member selection — a Builtin
// for a builtin primitive (so it compares against a union's builtin member), a
// Named for any other definition, and nil for a nil def.
func defType(def *ir.TypeDef) ir.Type {
	if def == nil {
		return nil
	}
	if def.Builtin {
		return &ir.Builtin{Name: def.Name}
	}
	return &ir.Named{Def: def}
}

// normalizeBuiltin rewrites a Named over a builtin definition to the Builtin form,
// leaving every other type unchanged — so a node type resolved as a nominal
// wrapper of a builtin compares against a union's builtin member.
func normalizeBuiltin(t ir.Type) ir.Type {
	if n, ok := t.(*ir.Named); ok && n.Def != nil && n.Def.Builtin {
		return &ir.Builtin{Name: n.Def.Name}
	}
	return t
}

// memberBacksKind reports whether a union member type can hold a value of the
// given kind: a builtin by its native descriptor, a nominal type by its
// underlying primitive (a Level over an integer). A composite member (a record,
// a union, a function) backs no scalar kind here — a record value reaches its
// member through its nominal static type, not this kind fallback.
func memberBacksKind(reg *builtin.Registry, m ir.Type, kind ir.ConstKind) bool {
	switch m := m.(type) {
	case *ir.Builtin:
		return scalarMatchesBuiltin(reg, &ir.Constant{Kind: kind}, m.Name)
	case *ir.Named:
		return m.Def != nil && defBacksKind(reg, m.Def, kind)
	default:
		return false
	}
}
