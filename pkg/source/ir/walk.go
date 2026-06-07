// This file holds the exhaustive walkers over the IR's sealed interfaces:
// WalkValues visits every value node of a graph and WalkBody every node of a
// lowered statement body. They are the traversal the consumers share — the
// editor's anchor index, the diagnostics' per-node folds — and, like the dump,
// they panic on an unhandled form rather than silently skipping it, so a new
// node kind cannot slip past the IR's consumers.

package ir

import "fmt"

// WalkValues visits v and every value node reachable inside it, parents before
// children, in field order. A nil value (a hole in a recovered graph) is
// skipped. fn returning false prunes the node's children (the node itself has
// already been visited).
func WalkValues(v Value, fn func(Value) bool) {
	if v == nil {
		return
	}
	if !fn(v) {
		return
	}
	switch v := v.(type) {
	case *Adapt:
		WalkValues(v.Value, fn)
	case *IntLiteral, *StringLiteral, *BoolLiteral, *DatetimeLiteral,
		*DurationLiteral, *NullValue, *SelfValue, *ParamRef, *LocalRef,
		*Reference, *EnumMemberValue, *AssocConstValue:
		// Leaves: nothing beneath.
	case *CollectionLiteral:
		for _, e := range v.Entries {
			WalkValues(e.Key, fn)
			WalkValues(e.Value, fn)
		}
	case *RecordValue:
		for _, f := range v.Fields {
			WalkValues(f.Value, fn)
		}
	case *Call:
		WalkValues(v.Receiver, fn)
		for _, a := range v.Args {
			WalkValues(a, fn)
		}
	case *FuncCall:
		for _, a := range v.Args {
			WalkValues(a, fn)
		}
	case *StaticCall:
		for _, a := range v.Args {
			WalkValues(a, fn)
		}
	case *Apply:
		WalkValues(v.Callee, fn)
		for _, a := range v.Args {
			WalkValues(a, fn)
		}
	case *FuncLiteral:
		WalkBody(v.Body, fn)
	case *FieldAccess:
		WalkValues(v.Receiver, fn)
	case *Conversion:
		for _, a := range v.Args {
			WalkValues(a, fn)
		}
	case *Await:
		WalkValues(v.Value, fn)
	case *Ternary:
		WalkValues(v.Cond, fn)
		WalkValues(v.Then, fn)
		WalkValues(v.Else, fn)
	case *RangeLit:
		WalkValues(v.Lower, fn)
		WalkValues(v.Upper, fn)
	default:
		panic(unhandledValueWalk(v))
	}
}

// WalkBody visits every value node a statement body carries, through every
// statement form, with the same parents-before-children order WalkValues
// keeps.
func WalkBody(body []Stmt, fn func(Value) bool) {
	for _, s := range body {
		switch s := s.(type) {
		case *Return:
			WalkValues(s.Value, fn)
		case *ExprStmt:
			WalkValues(s.Value, fn)
		case *Let:
			WalkValues(s.Value, fn)
		case *Assign:
			WalkValues(s.Value, fn)
		case *Switch:
			WalkValues(s.Scrutinee, fn)
			for _, arm := range s.Arms {
				for _, pat := range arm.Values {
					WalkValues(pat, fn)
				}
				WalkBody(arm.Body, fn)
			}
			WalkBody(s.Else, fn)
		case *Match:
			WalkValues(s.Scrutinee, fn)
			for _, arm := range s.Arms {
				WalkBody(arm.Body, fn)
			}
			WalkBody(s.Else, fn)
		case *If:
			WalkValues(s.Cond, fn)
			WalkBody(s.Then, fn)
			if s.ElseIf != nil {
				WalkBody([]Stmt{s.ElseIf}, fn)
			}
			WalkBody(s.Else, fn)
		case *For:
			WalkValues(s.Iter, fn)
			WalkBody(s.Body, fn)
		default:
			panic(unhandledStmt(s))
		}
	}
}

func unhandledValueWalk(v Value) string {
	return fmt.Sprintf("ir: unhandled Value kind %T in walk", v)
}
