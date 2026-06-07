package lsp

import (
	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// letHover describes the block-local denoted at offset: a let-bound local (its
// declaration or a reference to it) or a match arm's narrowed binding. The type
// is the binding's settled type carried on the resolved ir.Let or ir.MatchArm —
// for a let the annotation when written, otherwise the value's inferred type; for
// a match arm the member type the arm narrowed it to (Coin in `Coin c`) — so
// hovering a mutable local or a narrowed binding reads the same way as hovering a
// parameter. It runs after the parameter hovers, so a parameter a binding happens
// to share a name with is described as the parameter at its own positions; inside
// the body the binding shadows it, which the enclosing-body match below honours.
func letHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	buf := doc.Buffer()
	tok, name, ok := identAt(doc.AST().Concrete().Tree(), buf, offset)
	if !ok {
		return nil
	}

	body, found := enclosingBody(doc, offset, trees)
	if !found {
		return nil
	}
	typ, bound := letTypeOf(body, name)
	if !bound || typ == nil || typ == ir.Invalid {
		return nil
	}
	r := toRange(buf, tok.Offset(), tok.End())
	return &protocol.Hover{
		Contents: protocol.MarkupContent{
			Kind:  protocol.Markdown,
			Value: "```masterbelt\n" + name + ": " + typ.String() + "\n```",
		},
		Range: &r,
	}
}

// enclosingBody returns the resolved statement body of the method or function
// whose declaration spans offset, or false when the cursor is not inside one.
// The body is matched through the resolution's syntax link (ir.Method.Syntax /
// ir.Function.Syntax), the same pairing the parameter hovers navigate by.
func enclosingBody(doc view, offset int, trees map[cst.Green]cst.Tree) ([]ir.Stmt, bool) {
	m := doc.Module()
	for _, def := range m.Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			if t, ok := trees[irm.Syntax.Syntax()]; ok && within(t, offset) {
				return irm.Body, true
			}
		}
	}
	for _, fn := range m.Funcs {
		if fn.Syntax == nil {
			continue
		}
		if t, ok := trees[fn.Syntax.Syntax()]; ok && within(t, offset) {
			return fn.Body, true
		}
	}
	return nil, false
}

// letTypeOf walks a resolved body for a binding of name and returns its type. It
// descends control-flow blocks (a binding may sit inside an if, a switch arm, or
// a match arm) and recognizes both a let-bound local and a match arm's narrowed
// binding (Coin in `Coin c`), taking the first binding it finds — enough for a
// hover, which only needs the binding's declared type, not which shadow a given
// position sees.
func letTypeOf(body []ir.Stmt, name string) (ir.Type, bool) {
	for _, s := range body {
		switch s := s.(type) {
		case *ir.Let:
			if s.Name == name {
				return s.Type, true
			}
		case *ir.If:
			if t, ok := letTypeOfIf(s, name); ok {
				return t, true
			}
		case *ir.Switch:
			for _, arm := range s.Arms {
				if t, ok := letTypeOf(arm.Body, name); ok {
					return t, true
				}
			}
			if t, ok := letTypeOf(s.Else, name); ok {
				return t, true
			}
		case *ir.Match:
			for _, arm := range s.Arms {
				// The arm's own binding narrows name to the arm's member type for
				// the arm body, so it is reported before descending — a reference
				// to the binding inside the body reads the narrowed type.
				if arm.Name == name && arm.Type != nil {
					return arm.Type, true
				}
				if t, ok := letTypeOf(arm.Body, name); ok {
					return t, true
				}
			}
			if t, ok := letTypeOf(s.Else, name); ok {
				return t, true
			}
		case *ir.For:
			// The loop variable binds name to its element type for the loop body,
			// so it is reported before descending — a reference to it inside the
			// body reads that type.
			if s.Var == name && s.VarType != nil {
				return s.VarType, true
			}
			if t, ok := letTypeOf(s.Body, name); ok {
				return t, true
			}
		}
	}
	return nil, false
}

// letTypeOfIf descends an if's then body, its else-if chain, and its else body
// for a let binding of name.
func letTypeOfIf(s *ir.If, name string) (ir.Type, bool) {
	if t, ok := letTypeOf(s.Then, name); ok {
		return t, true
	}
	if s.ElseIf != nil {
		if t, ok := letTypeOfIf(s.ElseIf, name); ok {
			return t, true
		}
	}
	if t, ok := letTypeOf(s.Else, name); ok {
		return t, true
	}
	return nil, false
}
