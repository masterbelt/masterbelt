package ast

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Render renders an expression in surface syntax, inverting the operator
// desugaring: a call of an operator method renders as the operator form it
// lowered from (1.add(2) back to 1 + 2), parenthesized only where precedence
// demands. Diagnostics quote an expression back to the user through it — the
// analysis works over the position-independent AST, so the original source
// text is out of reach, and this canonical form is stable under whitespace
// and comment edits besides.
//
// The operator spellings and precedences mirror the desugaring in
// parser/abstract and the precedence table in parser/concrete; a round-trip
// test in parser/abstract pins the three to one another.
func Render(e Expr) string {
	r := &renderer{}
	r.expr(e, 0)
	return r.b.String()
}

// Anchor is one value-bearing sub-expression of a rendered expression and the
// rune column its value reads best under: an identifier or self anchors at its
// own start, a member access and a method call at the member name, and an
// operator form at the operator symbol — the spot a power-assert diagram
// points its pipe at.
type Anchor struct {
	Expr Expr
	Col  int // rune column within the rendered text
}

// RenderTrace renders e exactly as Render does and additionally returns the
// anchors of its value-bearing sub-expressions, in render order. Literals and
// collection literals carry no anchor — their value is their own spelling.
func RenderTrace(e Expr) (string, []Anchor) {
	r := &renderer{trace: true}
	r.expr(e, 0)
	return r.b.String(), r.anchors
}

// binaryOps maps each binary operator method to its surface spelling and
// precedence (higher binds tighter, matching parser/concrete's binaryPrec, but
// shifted up by one to leave room for the range operator at precedence 1 between
// the ternary and "||").
var binaryOps = map[string]struct {
	sym  string
	prec int
}{
	"oror": {"||", 2},
	"anan": {"&&", 3},
	"eql":  {"==", 4},
	"neq":  {"!=", 4},
	"lt":   {"<", 4},
	"lteq": {"<=", 4},
	"gt":   {">", 4},
	"gteq": {">=", 4},
	"add":  {"+", 5},
	"sub":  {"-", 5},
	"mul":  {"*", 6},
	"div":  {"/", 6},
	"rem":  {"%", 6},
}

// unaryOps maps each prefix operator method to its surface spelling.
var unaryOps = map[string]string{"pos": "+", "neg": "-", "not": "!"}

// The ternary "?:" binds loosest (precedence 0). The range operator binds one
// step tighter (precedence 1) — looser than every binary operator, tighter than
// the ternary — so it parenthesizes inside any binary/unary/postfix context but
// not as a ternary branch. A prefix operator binds tighter than any binary
// operator, and a postfix member access or call tighter still — an
// operator-formed receiver needs the grouping parentheses there.
const (
	precTernary = 0
	precRange   = 1
	precUnary   = 7
	precPostfix = 8
)

// renderer accumulates the rendered text, tracking the rune column so anchors
// can be recorded as the writes happen.
type renderer struct {
	b       strings.Builder
	col     int // runes written so far
	trace   bool
	anchors []Anchor
}

func (r *renderer) str(s string) {
	r.b.WriteString(s)
	r.col += utf8.RuneCountInString(s)
}

// anchor records e's value anchor at the current column.
func (r *renderer) anchor(e Expr) {
	if r.trace {
		r.anchors = append(r.anchors, Anchor{Expr: e, Col: r.col})
	}
}

// expr renders e, parenthesizing it when its own binding is looser than
// minPrec — the binding the enclosing context requires.
func (r *renderer) expr(e Expr, minPrec int) {
	if r.leafExpr(e) {
		return
	}
	switch x := e.(type) {
	case *CollectionLit:
		r.collectionLit(x)
	case *RecordLit:
		r.recordLit(x)
	case *MemberExpr:
		r.expr(x.Receiver, precPostfix)
		r.str(".")
		r.anchor(x)
		r.str(x.Member.Name)
	case *CallExpr:
		r.call(x, minPrec)
	case *AwaitExpr:
		r.awaitExpr(x, minPrec)
	case *TernaryExpr:
		r.ternaryExpr(x, minPrec)
	case *RangeExpr:
		r.rangeExpr(x, minPrec)
	case *FuncLit:
		r.funcLit(x)
	default:
		r.str("<expr>")
	}
}

// leafExpr renders the atom expression forms — the literals, self, and a plain
// identifier — whose rendering is precedence-independent and needs no
// parentheses, returning true when it handled e. A composite form (or a kind
// with no atom rendering) returns false, leaving the caller's switch to render
// it. nil renders the missing-expression marker.
func (r *renderer) leafExpr(e Expr) bool {
	switch x := e.(type) {
	case nil:
		r.str("<missing>")
	case *IntLit:
		r.str(x.Text)
	case *StringLit:
		r.str(fmt.Sprintf("%q", x.Value))
	case *DatetimeLit:
		r.str(x.Text)
	case *DurationLit:
		r.str(x.Text)
	case *BoolLit:
		if x.Value {
			r.str("true")
		} else {
			r.str("false")
		}
	case *NullLit:
		r.str("null")
	case *SelfExpr:
		r.anchor(x)
		r.str("self")
	case *Identifier:
		r.anchor(x)
		r.str(x.Name)
	default:
		return false
	}
	return true
}

// collectionLit renders a list or map literal: [a, b] or [k: v, ...].
func (r *renderer) collectionLit(x *CollectionLit) {
	r.str("[")
	for i, entry := range x.Entries {
		if i > 0 {
			r.str(", ")
		}
		if entry.Key != nil {
			r.expr(entry.Key, 0)
			r.str(": ")
		}
		r.expr(entry.Value, 0)
	}
	r.str("]")
}

// recordLit renders a record literal: Type{} for the empty form, Type{ f: v }
// otherwise (the type name is "" for the inferred form).
func (r *renderer) recordLit(x *RecordLit) {
	r.str(x.TypeName) // "" for the inferred form
	if len(x.Fields) == 0 {
		r.str("{}")
		return
	}
	r.str("{ ")
	for i, f := range x.Fields {
		if i > 0 {
			r.str(", ")
		}
		r.str(f.Name + ": ")
		r.expr(f.Value, 0)
	}
	r.str(" }")
}

// awaitExpr renders an await: it binds like a prefix operator over its operand's
// postfix chain.
func (r *renderer) awaitExpr(x *AwaitExpr, minPrec int) {
	paren := precUnary < minPrec
	if paren {
		r.str("(")
	}
	r.anchor(x)
	r.str("await ")
	r.expr(x.Value, precUnary)
	if paren {
		r.str(")")
	}
}

// ternaryExpr renders a ternary. It binds loosest and nests on the right: the
// condition and the then-branch render one level tighter (so a nested ternary
// there is parenthesized), while the else-branch renders at the ternary level —
// a chained a ? b : c ? d : e needs no parentheses around its tail.
func (r *renderer) ternaryExpr(x *TernaryExpr, minPrec int) {
	paren := precTernary < minPrec
	if paren {
		r.str("(")
	}
	r.expr(x.Cond, precTernary+1)
	r.str(" ")
	r.anchor(x)
	r.str("? ")
	r.expr(x.Then, precTernary+1)
	r.str(" : ")
	r.expr(x.Else, precTernary)
	if paren {
		r.str(")")
	}
}

// rangeExpr renders a range. It binds looser than every binary operator and
// tighter than the ternary, and is non-associative: both bounds render one step
// tighter (precRange+1), so a range bound that is itself a range or a ternary is
// parenthesized, while an arithmetic bound (binding tighter) is not.
func (r *renderer) rangeExpr(x *RangeExpr, minPrec int) {
	paren := precRange < minPrec
	if paren {
		r.str("(")
	}
	r.expr(x.Lower, precRange+1)
	r.anchor(x)
	if x.HalfOpen {
		r.str("...")
	} else {
		r.str("..")
	}
	r.expr(x.Upper, precRange+1)
	if paren {
		r.str(")")
	}
}

// call renders a call expression: an operator method back as its operator
// form, anything else as callee(args). The call's value anchors at the
// operator symbol or the callee's name — the callee itself is not a value, so
// it records no anchor of its own.
func (r *renderer) call(x *CallExpr, minPrec int) {
	if m, ok := x.Callee.(*MemberExpr); ok {
		if r.operatorCall(x, m, minPrec) {
			return
		}
		r.expr(m.Receiver, precPostfix)
		r.str(".")
		r.anchor(x)
		r.str(m.Member.Name)
	} else if id, ok := x.Callee.(*Identifier); ok {
		// A conversion (Level(50)): the name is a type, not a value.
		r.anchor(x)
		r.str(id.Name)
	} else {
		r.expr(x.Callee, precPostfix)
		r.anchor(x)
	}
	r.str("(")
	for i, a := range x.Arguments {
		if i > 0 {
			r.str(", ")
		}
		r.expr(a, 0)
	}
	r.str(")")
}

// operatorCall renders a call of an operator method (member callee m) back as
// its surface operator form — a one-argument binary operator or a no-argument
// unary one — returning true when it handled the call. A member call that is not
// an operator form returns false, leaving the caller to render the method call.
func (r *renderer) operatorCall(x *CallExpr, m *MemberExpr, minPrec int) bool {
	if op, ok := binaryOps[m.Member.Name]; ok && len(x.Arguments) == 1 {
		paren := op.prec < minPrec
		if paren {
			r.str("(")
		}
		r.expr(m.Receiver, op.prec)
		r.str(" ")
		r.anchor(x)
		r.str(op.sym + " ")
		// prec+1 on the right keeps the rendering left-associative,
		// exactly as the parser is.
		r.expr(x.Arguments[0], op.prec+1)
		if paren {
			r.str(")")
		}
		return true
	}
	if sym, ok := unaryOps[m.Member.Name]; ok && len(x.Arguments) == 0 {
		paren := precUnary < minPrec
		if paren {
			r.str("(")
		}
		r.anchor(x)
		r.str(sym)
		r.expr(m.Receiver, precUnary)
		if paren {
			r.str(")")
		}
		return true
	}
	return false
}

// funcLit renders a function literal on one line — statements joined by ";" —
// since the rendering is quoted inside a diagnostic message.
func (r *renderer) funcLit(x *FuncLit) {
	r.str("fn(")
	for i, p := range x.Params {
		if i > 0 {
			r.str(", ")
		}
		r.str(p.Name)
		if p.Type != nil {
			r.str(": " + renderType(p.Type))
		}
	}
	r.str(")")
	if x.Result != nil {
		r.str(": " + renderType(x.Result))
	}
	if len(x.Body) == 0 {
		r.str(" {}")
		return
	}
	r.str(" {")
	for i, s := range x.Body {
		if i > 0 {
			r.str(";")
		}
		switch s := s.(type) {
		case *ReturnStmt:
			r.str(" return ")
			r.expr(s.Value, 0)
		case *ExprStmt:
			r.str(" ")
			r.expr(s.X, 0)
		case *LetStmt:
			r.str(" let " + s.Name)
			if s.Type != nil {
				r.str(": " + renderType(s.Type))
			}
			r.str(" = ")
			r.expr(s.Value, 0)
		case *AssignStmt:
			r.str(" ")
			r.expr(s.Target, 0)
			r.str(" = ")
			r.expr(s.Value, 0)
		default:
			// A control-flow statement (switch/if) does not appear in the
			// expression traces this renderer produces, and a kind added later
			// must not vanish — render a visible marker instead of nothing.
			r.str(" " + stmtMarker(s))
		}
	}
	r.str(" }")
}

// stmtMarker is the placeholder the expression renderer emits for a statement
// kind it has no inline form for, so an unhandled kind is visible rather than
// silently absent from the trace.
func stmtMarker(s Stmt) string {
	return fmt.Sprintf("<%T>", s)
}

// renderType renders a type expression in source-like form: int8, Optional<T>,
// A | B, { id: int8 }, fn(src: T): R — the type half of the surface rendering,
// used where a rendered expression carries an annotation.
func renderType(t TypeExpr) string {
	switch t := t.(type) {
	case nil:
		return "<missing>"
	case *NamedType:
		name := t.Name
		if t.Namespace != "" {
			name = t.Namespace + "." + t.Name
		}
		// The field-type-projection segments dotted onto the head (Order.customer.id)
		// come before any generic arguments: the parser associates the arguments
		// with the whole dotted head, so Order.customer.id<string> must render with
		// the segments first or it re-parses with .id left dangling.
		if len(t.Projections) > 0 {
			name += "." + strings.Join(t.Projections, ".")
		}
		if len(t.Args) > 0 {
			args := make([]string, len(t.Args))
			for i, a := range t.Args {
				args[i] = renderType(a)
			}
			name += "<" + strings.Join(args, ", ") + ">"
		}
		return name
	case *UnionType:
		parts := make([]string, len(t.Members))
		for i, m := range t.Members {
			parts[i] = renderType(m)
		}
		return strings.Join(parts, " | ")
	case *RecordType:
		parts := make([]string, len(t.Fields))
		for i, f := range t.Fields {
			parts[i] = f.Name + ": " + renderType(f.Type)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case *FuncType:
		params := make([]string, len(t.Params))
		for i, p := range t.Params {
			params[i] = p.Name + ": " + renderType(p.Type)
		}
		return "fn(" + strings.Join(params, ", ") + "): " + renderType(t.Result)
	case *BuiltinType:
		if len(t.Args) == 0 {
			return "builtin"
		}
		args := make([]string, len(t.Args))
		for i, a := range t.Args {
			args[i] = renderType(a)
		}
		return "builtin<" + strings.Join(args, ", ") + ">"
	default:
		return "Type(?)"
	}
}
